// Package hitlControllers is the HTTP controller for Human-in-the-Loop tasks
// (HumanTask). Unlike the other entities there is deliberately NO create/upsert
// route: HITL tasks are only ever created by the inflow svc handler when a
// running workflow reaches a `humanInLoop` node (see inflow/port.go). The API
// exposes reads (list / open), the answer / message / close actions, and delete.
package hitlControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.HumanTaskRepository
}

// Register mounts the /hitl route group. Paths mirror the web app's hitlApi.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.HumanTasks()}

	g := api.Group("/hitl")
	g.Get("", ctl.list)                    // list (?status=&search=&page=&per_page=)
	g.Get("/id/:id", ctl.getByID)          // open
	g.Post("/id/:id/answer", ctl.answer)   // action: answer a question
	g.Post("/id/:id/message", ctl.message) // action: append a chat message
	g.Post("/id/:id/close", ctl.close)     // action: close (workflow finishes here)
	g.Delete("/id/:id", ctl.deleteByID)    // delete
}
