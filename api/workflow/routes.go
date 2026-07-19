// Package workflowControllers is the HTTP controller for workflows (FlowRecord).
package workflowControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.WorkflowRepository
}

// Register mounts the /flow route group. Paths mirror the web app's flowsApi:
// POST upsert, GET list, GET/DELETE by id under /id/:id.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.Workflows()}

	g := api.Group("/flow")
	g.Post("", ctl.upsert)
	g.Get("", ctl.list)
	g.Get("/id/:id", ctl.getByID)
	g.Delete("/id/:id", ctl.deleteByID)
}
