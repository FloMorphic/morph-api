// Package processControllers is the HTTP controller for process runs (Process).
//
// Rows are written by the inflow layer when a process request is sent and closed
// out from the engine's proc.finish event — so, like HITL, there is no plain
// upsert route. The API exposes a launch action (POST /process, which records a
// row and dispatches the run to the engine), reads (list / get) and delete.
package processControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	store repository.Store
}

// Register mounts the /process route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{store: store}

	g := api.Group("/process")
	g.Post("", ctl.start)                 // launch a workflow run
	g.Get("", ctl.list)                   // list (?status=&pid=&search=&page=&per_page=)
	g.Get("/id/:id", ctl.getByID)         // read one run
	g.Post("/id/:id/stop", ctl.stop)      // stop a run on the engine
	g.Delete("/id/:id", ctl.deleteByID)   // delete
}
