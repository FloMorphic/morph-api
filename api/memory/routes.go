// Package memoryControllers is the HTTP controller for memory stores (vector /
// document). Paths mirror the web app's memoryApi: list/create at /memory,
// delete at /memory/:id.
package memoryControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.MemoryRepository
}

// Register mounts the /memory route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.Memory()}

	g := api.Group("/memory")
	g.Get("", ctl.list)
	g.Post("", ctl.create)
	g.Get("/:id", ctl.getByID)
	g.Delete("/:id", ctl.deleteByID)
}
