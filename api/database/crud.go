package databaseControllers

import (
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/postgres"
	"github.com/gofiber/fiber/v3"
)

// state handles GET /database — the live database, a health check on it, and the
// set of drivers and entity keys the panel can offer.
func (ctl *controller) state(c fiber.Ctx) error {
	return etc.OK(c, ctl.snapshot(c))
}

// test handles POST /database/test — probe a candidate database and report what
// was found. It is read-only with respect to the candidate: the probe creates no
// application table, so testing a URI that turns out to be the wrong server
// leaves nothing behind in it.
func (ctl *controller) test(c fiber.Ctx) error {
	target, err := bindTarget(c)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	cfg := repository.Config{Driver: target.Driver, Source: target.Source}.Normalized()
	res, err := repository.Probe(c.Context(), cfg)
	out := models.DatabaseProbe{
		OK:       err == nil,
		Driver:   res.Driver,
		Server:   res.Server,
		Writable: res.Writable,
		Vector:   res.Vector,
		Notes:    res.Notes,
	}
	if err != nil {
		out.Error = err.Error()
		if out.Driver == "" {
			out.Driver = cfg.Driver
		}
	}
	// A failed probe is a complete answer to "can I use this?", not a failed
	// request — the panel renders the reason beside the field the operator typed.
	return etc.OK(c, out)
}

// switchTo handles POST /database/switch — move this instance onto the given
// database. The candidate is probed first, then opened (which applies the schema
// and seeds the palette), and only a success repoints the live store. The
// selection is persisted so a restart comes back on the same database.
//
// Existing records are NOT copied: the instance starts from whatever the target
// already holds.
func (ctl *controller) switchTo(c fiber.Ctx) error {
	if ctl.sw == nil {
		return etc.Fail(c, fiber.StatusConflict,
			"this instance cannot switch databases at run time; set DB_DRIVER/DB_SOURCE and restart")
	}
	target, err := bindTarget(c)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	cfg := repository.Config{Driver: target.Driver, Source: target.Source}.Normalized()
	if err := cfg.Validate(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}

	// Probe before committing: a URI that cannot be reached, or a role that
	// cannot create tables, is caught while the instance is still on its current
	// database.
	if res, err := repository.Probe(c.Context(), cfg); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	} else if !res.Writable {
		return etc.Fail(c, fiber.StatusBadRequest,
			"this connection cannot create tables in the target database")
	}

	if err := ctl.sw.Switch(cfg); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	// Persist only after the live switch succeeded, so a saved selection is
	// always one this instance has actually run on.
	if _, err := repository.SaveConfig(env.GetDBConfigFile(), cfg); err != nil {
		// The switch stands — the instance IS on the new database — but it will
		// not survive a restart. Say so rather than implying a clean result.
		return etc.Send(c, fiber.StatusOK, fiber.Map{
			"state":   ctl.snapshot(c),
			"warning": "switched, but the selection could not be saved: " + err.Error(),
		}, nil)
	}
	return etc.OK(c, ctl.snapshot(c))
}

// reset handles POST /database/reset — forget the panel's selection and go back
// to the database the environment names (DB_DRIVER/DB_SOURCE, i.e. the sqlite
// default unless configured otherwise).
func (ctl *controller) reset(c fiber.Ctx) error {
	if ctl.sw == nil {
		return etc.Fail(c, fiber.StatusConflict,
			"this instance cannot switch databases at run time; set DB_DRIVER/DB_SOURCE and restart")
	}
	if err := repository.RemoveConfig(env.GetDBConfigFile()); err != nil {
		return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
	}
	envCfg := repository.Config{Driver: env.GetDBDriver(), Source: env.GetDBSource()}.Normalized()
	if err := ctl.sw.Switch(envCfg); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	return etc.OK(c, ctl.snapshot(c))
}

// bindTarget reads and trims a target from the request body.
func bindTarget(c fiber.Ctx) (models.DatabaseTarget, error) {
	var in models.DatabaseTarget
	if err := c.Bind().Body(&in); err != nil {
		return in, errInvalidBody
	}
	in.Driver = strings.ToLower(strings.TrimSpace(in.Driver))
	in.Source = strings.TrimSpace(in.Source)
	return in, nil
}

// errInvalidBody is the one binding failure this group can have.
var errInvalidBody = fiber.NewError(fiber.StatusBadRequest, "invalid database payload")

// snapshot assembles the panel's view of the live database, including a health
// check. The source is redacted: it travels to a browser, and the password in a
// connection URI is a write-only field exactly like the Connect tokens.
func (ctl *controller) snapshot(c fiber.Ctx) models.DatabaseState {
	live := repository.Config{Driver: env.GetDBDriver(), Source: env.GetDBSource()}.Normalized()
	if ctl.sw != nil {
		live = ctl.sw.Config()
	}

	out := models.DatabaseState{
		Driver:        live.Driver,
		Source:        repository.RedactSource(live.Source),
		Origin:        "env",
		UpdatedAt:     live.UpdatedAt,
		Drivers:       repository.Drivers(),
		Switchable:    ctl.sw != nil,
		VectorSource:  vectorSource(),
		EntityKeys:    postgres.LocalEntityKeys,
		ConfigFile:    env.GetDBConfigFile(),
		LocalEntities: ctl.localEntities(),
	}
	// The saved file is what a restart would read, so matching it is what makes
	// the live selection the panel's rather than the environment's.
	if saved, err := repository.LoadConfig(env.GetDBConfigFile()); err == nil && saved != nil {
		if saved.Driver == live.Driver && saved.Source == live.Source {
			out.Origin = "panel"
			if out.UpdatedAt == 0 {
				out.UpdatedAt = saved.UpdatedAt
			}
		}
	}

	started := time.Now()
	if err := repository.PingStore(c.Context(), ctl.store); err != nil {
		out.Error = err.Error()
	} else {
		out.Connected = true
	}
	out.LatencyMs = time.Since(started).Milliseconds()
	return out
}

// localEntities asks the live backend which entities it serves from sqlite — a
// capability only the postgres backend has (see postgres.LocalEntityKeys).
func (ctl *controller) localEntities() []string {
	var live repository.Store = ctl.store
	if ctl.sw != nil {
		live = ctl.sw.Live()
	}
	if l, ok := live.(interface{ LocalEntities() []string }); ok {
		return l.LocalEntities()
	}
	return nil
}

// vectorSource reports where vector indexes live, or "" when vector memory is
// switched off.
func vectorSource() string {
	src := env.GetVectorDBSource()
	if src == "" || strings.EqualFold(src, postgres.VectorDisabled) {
		return ""
	}
	return src
}
