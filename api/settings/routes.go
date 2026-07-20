// Package settingsControllers is the HTTP controller for node settings profiles
// (NodeSetting) — a reusable, named key/value config bound to a node's kind /
// plugin identity (`nodeUniqId`). A node may own several profiles; a canvas node
// instance references one by id. Full CRUD, page-paginated, with an optional
// `?node=` filter so the node drawer can fetch just that node's profiles.
package settingsControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.NodeSettingRepository
}

// Register mounts the /settings route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.NodeSettings()}

	g := api.Group("/settings")
	g.Post("", ctl.upsert)              // create (no id) or update
	g.Get("", ctl.list)                 // list (?page=&per_page=&search=&node=)
	g.Get("/id/:id", ctl.getByID)       // open one profile
	g.Delete("/id/:id", ctl.deleteByID) // delete
}
