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

	// Record-level data surface used by the web app's store data browser.
	// Document stores: CRUD over rows. Vector stores: embed-backed search/index.
	g.Get("/:id/records", ctl.listRecords)
	g.Post("/:id/records", ctl.createRecord)
	g.Put("/:id/records/:rid", ctl.updateRecord)
	g.Delete("/:id/records/:rid", ctl.deleteRecord)
	g.Post("/:id/search", ctl.searchVectors)
	g.Post("/:id/vectors", ctl.indexVector)
}
