package workflowControllers

import (
	"encoding/json"
	"errors"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/flowfile"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// POST /flow/import and GET /flow/id/:id/export — the REST twins of the web
// app's "Import workflow" dialog and "Export" button, over the portable
// workflow document (see package flowfile for the format and why it, and not
// the raw FlowRecord, is what leaves and enters an install).
//
// This is what lets another system install a flow without the browser: an
// operation package's flow files go through here, one call per flow, and the
// response names any plugin action this install cannot serve so the caller
// (Venapce's operation installer) can tell the operator what to install.

type importInput struct {
	// Workflow id to overwrite (a re-install); empty creates a new flow.
	ID string `json:"id"`
	// Overrides the document's own title when set.
	Title string `json:"title"`
	// Overrides the document's own tags when present — what an installer that
	// knows which framework this flow belongs to stamps on it, so the flow
	// picker on the far side can narrow to that framework. Absent keeps the
	// document's tags; `[]` imports the flow with none.
	Tags []string `json:"tags"`
	// Overrides the document's own preferences (its entry form) when present.
	// Absent keeps the document's; `{}` imports the flow without one.
	Preferences *models.FlowPreferences `json:"preferences"`
	// Plan and report without saving.
	DryRun bool `json:"dryRun"`
	// The export document itself.
	Workflow json.RawMessage `json:"workflow"`
}

// importFlow handles POST /flow/import.
func (ctl *controller) importFlow(c fiber.Ctx) error {
	var in importInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid import payload")
	}
	if len(in.Workflow) == 0 {
		return etc.Fail(c, fiber.StatusBadRequest, `"workflow" (the export document) is required`)
	}
	doc, err := flowfile.Parse(in.Workflow)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	res, err := flowfile.Import(c.Context(), ctl.store, flowfile.Input{Doc: doc, ID: in.ID, Title: in.Title, Tags: in.Tags, Preferences: in.Preferences, DryRun: in.DryRun})
	if err != nil {
		if errors.Is(err, flowfile.ErrInvalid) {
			return etc.Fail(c, fiber.StatusBadRequest, err.Error())
		}
		return etc.FailFromRepo(c, err, "workflow not saved")
	}
	if !res.DryRun {
		wslog.Emit("flow.changed", fiber.Map{"id": res.Flow.ID, "source": "import"})
	}
	return etc.OK(c, fiber.Map{
		"ok":             true,
		"flow":           res.Flow,
		"problems":       res.Problems,
		"missingActions": res.MissingActions,
		"unboundNodes":   res.UnboundNodes,
		"compileError":   res.CompileError,
		"dryRun":         res.DryRun,
	})
}

// exportFlow handles GET /flow/id/:id/export: the saved flow as a portable
// document, ready to be written to a file or fed back to /flow/import.
func (ctl *controller) exportFlow(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	return etc.OK(c, flowfile.Export(c.Context(), ctl.store, *rec))
}
