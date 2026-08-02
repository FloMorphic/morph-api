package extensionControllers

import (
	"encoding/json"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	InfraSpaces "github.com/Inflowenger/inflow-fusion/spaces"
	svcHandler "github.com/Inflowenger/inflow-fusion/svcHandler"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

// upsert handles POST /extension — create (no id) or update a palette node.
func (ctl *controller) upsert(c fiber.Ctx) error {
	var input models.ExtensionRecord
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid extension payload")
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "name is required")
	}
	if input.Kind == "" {
		input.Kind = models.KindExtension
	}
	if input.Kind != models.KindBuiltin && input.Kind != models.KindExtension {
		return etc.Fail(c, fiber.StatusBadRequest, "kind must be 'builtin' or 'extension'")
	}
	// The plugin id of an imported plugin belongs to the server, not the caller
	// (see resolvePluginID).
	if input.Kind == models.KindExtension {
		input.PluginID = ctl.resolvePluginID(c, input.ID, input.Name)
	}
	if err := ctl.repo.Upsert(c.Context(), &input); err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	return etc.OK(c, input)
}

// resolvePluginID decides the inflowv1 identity of an imported plugin. The
// caller never gets a say: a fresh row is issued one, and an existing row keeps
// the one it already has.
//
// Generated rather than user-chosen because the id is not a label — it is an
// address. Every subject the plugin owns is `inflow.v1.<pluginId>.…`, and the
// credential minted for it is scoped to exactly those subjects, so two people
// importing the same plugin under the same name would otherwise end up sharing
// (and able to answer for) each other's traffic.
//
// Never reassigned on update — not even when the row is renamed: it is what the
// running plugin was configured with and what its credential is bound to, so
// changing it would orphan a live process.
func (ctl *controller) resolvePluginID(c fiber.Ctx, id, name string) string {
	if id != "" {
		if existing, err := ctl.repo.GetByID(c.Context(), id); err == nil && existing.PluginID != "" {
			return existing.PluginID
		}
	}
	return newPluginID(name)
}

// newPluginID is `<name>-<uuid>`: the uuid carries the uniqueness, the name
// prefix is there so the id is recognisable where it actually shows up — NATS
// subjects, plugin logs, the dotenv on the user's disk.
//
// The name half is slugified rather than used verbatim because the id becomes a
// subject token: a dot would split the subject, and `*` / `>` / whitespace would
// turn it into a wildcard or break the parse. It is also capped, since the full
// id is repeated in every subject.
func newPluginID(name string) string {
	return slug(truncate(name, pluginIDNameMax), "plugin") + "-" + uuid.NewString()
}

// pluginIDNameMax bounds the readable half of a plugin id.
const pluginIDNameMax = 32

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// list handles GET /extension — page-based listing with total count. An optional
// `?kind=builtin|extension` scopes to one origin (the admin panel lists builtins,
// the extension portal lists extensions); the palette omits it to get both.
func (ctl *controller) list(c fiber.Ctx) error {
	var q models.PaginationParams
	if err := c.Bind().Query(&q); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	if err := q.Normalize(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	kind := strings.TrimSpace(c.Query("kind"))
	if kind != "" && kind != string(models.KindBuiltin) && kind != string(models.KindExtension) {
		return etc.Fail(c, fiber.StatusBadRequest, "kind must be 'builtin' or 'extension'")
	}
	items, total, err := ctl.repo.List(c.Context(), repository.ListParams{
		Offset: q.Offset(),
		Limit:  q.PerPage,
		Search: q.Search,
		Kind:   kind,
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "extensions not found")
	}
	return etc.OK(c, models.NewPage(items, total, q))
}

// getByID handles GET /extension/id/:id.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	return etc.OK(c, rec)
}

// deleteByID handles DELETE /extension/id/:id. Removing a plugin's registration
// row takes the palette rows synced from its actions with it — they only exist
// to describe that plugin, so leaving them behind would strand nodes pointing at
// an id nothing is registered under.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	rec, err := ctl.repo.GetByID(c.Context(), id)
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	actions := 0
	if rec.Action == "" && rec.PluginID != "" {
		if actions, err = ctl.repo.DeletePluginActions(c.Context(), rec.PluginID); err != nil {
			return etc.FailFromRepo(c, err, "extension not found")
		}
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id, "actionsRemoved": actions}, nil)
}

// extrinsics handles GET /extension/extrinsics — the backend-registered extrinsic
// services (topicKey -> subject template) an extrinsic extension can bind to.
func (ctl *controller) extrinsics(c fiber.Ctx) error {
	return etc.OK(c, svcHandler.AllSvcSubjects())
}

// --- Live inflowv1 fetches (KindExtension only) ---------------------------
//
// These proxy the plugin over NATS through inflow-fusion. Nothing is stored: the
// intro/settings/actions/forms are always read live from the connected plugin.

func (ctl *controller) intro(c fiber.Ctx) error {
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginIntro(pluginID)
	})
}

func (ctl *controller) settings(c fiber.Ctx) error {
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginSettings(pluginID)
	})
}

func (ctl *controller) actions(c fiber.Ctx) error {
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginActions(pluginID)
	})
}

func (ctl *controller) actionForm(c fiber.Ctx) error {
	method := c.Params("method")
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginActionForm(pluginID, method)
	})
}

// proxyByPlugin resolves the extension's inflowv1 plugin id, runs the given fetch
// over NATS, and returns the plugin's raw JSON inside the standard envelope. It
// 400s for a non-extension row and 502s when the runtime/plugin is unreachable.
func (ctl *controller) proxyByPlugin(c fiber.Ctx, fetch func(pluginID string) ([]byte, error)) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "extension has no plugin id (not an inflowv1 plugin)")
	}
	raw, err := fetch(rec.PluginID)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	return etc.OK(c, json.RawMessage(raw))
}
