// Package promptControllers is the HTTP controller for prompt templates.
package promptControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.PromptRepository
}

// Register mounts the /prompt route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.Prompts()}

	g := api.Group("/prompt")
	g.Post("", ctl.upsert)
	g.Get("", ctl.list)
	g.Get("/id/:id", ctl.getByID)
	g.Delete("/id/:id", ctl.deleteByID)
}
