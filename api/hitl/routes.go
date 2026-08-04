// Package hitlControllers is the HTTP controller for Human-in-the-Loop tasks
// (HumanTask). Unlike the other entities there is deliberately NO create/upsert
// route: HITL tasks are only ever created by the inflow svc handler when a
// running workflow reaches a `humanInLoop` node (see inflow/port.go). The API
// exposes reads (list / open), the answer / message / close actions, and delete.
//
// Closing is the one action with a consequence beyond the record: a task whose
// node parked its flow resumes that flow from the captured next nodes, which is
// why this controller holds the whole store and not just the task repository.
package hitlControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo  repository.HumanTaskRepository
	store repository.Store
}

// Register mounts the /hitl route group. Paths mirror the web app's hitlApi.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.HumanTasks(), store: store}

	g := api.Group("/hitl")
	g.Get("", ctl.list)                    // list (?status=&search=&page=&per_page=)
	g.Get("/id/:id", ctl.getByID)          // open
	g.Post("/id/:id/answer", ctl.answer)   // action: answer a question
	g.Post("/id/:id/message", ctl.message) // action: append a chat message (no bot)
	g.Post("/id/:id/start", ctl.start)     // action: open the session — bot's first turn
	g.Post("/id/:id/chat", ctl.chat)       // action: send a turn, get the bot's reply
	g.Post("/id/:id/close", ctl.close)     // action: close (and resume a parked flow)
	g.Delete("/id/:id", ctl.deleteByID)    // delete
}
