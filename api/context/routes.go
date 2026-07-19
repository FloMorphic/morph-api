// Package contextControllers is the HTTP controller for context documents.
package contextControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.ContextRepository
}

// Register mounts the /context route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.Contexts()}

	g := api.Group("/context")
	g.Post("", ctl.upsert)
	g.Get("", ctl.list)
	g.Get("/id/:id", ctl.getByID)
	g.Delete("/id/:id", ctl.deleteByID)
}
