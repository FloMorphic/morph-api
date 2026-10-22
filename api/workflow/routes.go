// Package workflowControllers is the HTTP controller for workflows (FlowRecord).
package workflowControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.WorkflowRepository
	// The whole store, for the import / export path's extension lookups.
	store repository.Store
}

// Register mounts the /flow route group. Paths mirror the web app's flowsApi:
// POST upsert, GET list, GET/DELETE by id under /id/:id.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.Workflows(), store: store}

	g := api.Group("/flow")
	g.Post("", ctl.upsert)
	// Land a portable workflow document (the editor's export file) as a flow on
	// this install, and write one back out — the REST twins of the Import dialog
	// and the Export button (see import.go / package flowfile).
	g.Post("/import", ctl.importFlow)
	g.Get("", ctl.list)
	g.Get("/id/:id", ctl.getByID)
	// Relabel a flow (the tag filter's write side) without resending its graph.
	g.Put("/id/:id/tags", ctl.setTags)
	// The flow's preferences — its entry form — read and written on their own,
	// plus that form resolved for one launch (see preferences.go).
	g.Get("/id/:id/preferences", ctl.getPreferences)
	g.Put("/id/:id/preferences", ctl.setPreferences)
	g.Get("/id/:id/entry", ctl.getEntry)
	g.Get("/id/:id/export", ctl.exportFlow)
	g.Get("/id/:id/compile", ctl.compile)
	g.Delete("/id/:id", ctl.deleteByID)
}
