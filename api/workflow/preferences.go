package workflowControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// The flow's preferences — today its entry form, the input a flow asks for
// before it runs (models.FlowPreferences).
//
// They get their own routes for the same reason the tags do: an entry form is
// edited on its own, by a dialog that has no business re-sending (and
// possibly stale-overwriting) a 35 KB view_flow to change a schema. And the
// resolved form has to be served, not assembled by each client: "what should
// the fields start at" depends on the entry's prefill mode and on the context
// the run will go against, and the launcher, an agent over MCP and an
// installer must all get the same answer.

// setPreferencesInput is the body of PUT /flow/id/:id/preferences. The
// preferences object is taken as sent — an `entry` with a schema sets the
// form, an absent/empty `entry` clears it — which is also what makes the route
// its own undo.
type setPreferencesInput struct {
	Preferences *models.FlowPreferences `json:"preferences"`
}

// getPreferences handles GET /flow/id/:id/preferences — the flow's entry form
// as stored, without its graph.
func (ctl *controller) getPreferences(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	prefs := rec.Preferences
	if prefs == nil {
		prefs = &models.FlowPreferences{}
	}
	return etc.OK(c, fiber.Map{"id": rec.ID, "preferences": prefs})
}

// setPreferences handles PUT /flow/id/:id/preferences — set or clear the entry
// form without round-tripping the graph.
func (ctl *controller) setPreferences(c fiber.Ctx) error {
	var in setPreferencesInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid preferences payload")
	}
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	prefs := in.Preferences
	if prefs == nil {
		// An explicit "no preferences" — the body said `{}` or `null`. Sending
		// nothing on this route can only mean clear; a save that wants to leave
		// them alone simply does not call it.
		prefs = &models.FlowPreferences{}
	}
	if err := validateEntry(prefs.Normalize()); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	rec.Preferences = prefs
	if err := ctl.repo.Upsert(c.Context(), rec); err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	wslog.Emit("flow.changed", fiber.Map{"id": rec.ID, "source": "api"})
	return etc.OK(c, fiber.Map{"id": rec.ID, "preferences": rec.Preferences})
}

// getEntry handles GET /flow/id/:id/entry?contextId= — the entry form resolved
// for a launch: the declared form, the context path its answers land at, and
// the values the fields should start at (the entry's defaults, or — in live
// mode, against a given context — what already sits at that path).
//
// A flow that declares no entry answers with `entry: null`, which is a launcher's
// cue to run it the way it always did rather than an error.
func (ctl *controller) getEntry(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	view := inflow.ResolveEntryView(c.Context(), ctl.store, *rec, strings.TrimSpace(c.Query("contextId")))
	return etc.OK(c, view)
}

// validateEntry rejects an entry form that could not be honoured at launch
// time: a write target that does not name exactly one context location. The
// schema itself is left to the launch — a form is edited in pieces and a
// half-written schema must still be savable — but a bad target would only
// surface as a failed run, long after the dialog that set it closed.
func validateEntry(prefs *models.FlowPreferences) error {
	entry := prefs.EntryOrNil()
	if entry == nil {
		return nil
	}
	_, err := models.ParseContextPath(entry.TargetPath())
	return err
}
