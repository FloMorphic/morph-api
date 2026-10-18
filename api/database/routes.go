// Package databaseControllers is the HTTP controller for the admin panel's
// database screen: which database this instance persists to, whether it is
// answering, and — the point of the group — moving it to another one.
//
// SQLite is the default and is enough for a single-node install. A deployment
// that outgrows it (several API replicas, an existing managed postgres, ops who
// want their own backup and replication story) pastes a connection URI here
// instead of editing the environment and redeploying: the endpoint probes the
// candidate, opens it, and only then repoints the live store, so a mistyped URI
// costs an error message rather than the service.
//
// No data is copied. A switch moves the instance to an empty (or already
// populated) database, where the schema is created and the builtin palette and
// starter prompts are seeded as on any first run; moving existing records is a
// separate, per-table action.
package databaseControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

// switcher is the capability this group needs beyond reading: a store that can
// be repointed while the process runs (repository.Switchable). A store without
// it still gets the read endpoints, which then report a fixed selection.
type switcher interface {
	Config() repository.Config
	Switch(cfg repository.Config) error
	Live() repository.Store
}

type controller struct {
	store repository.Store
	// sw is nil when the wired store cannot be switched at run time.
	sw switcher
}

// Register mounts the /database route group. It sits inside the same optional
// bearer gate as the other CRUD groups: repointing persistence is an admin
// action, not a public one.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{store: store}
	if sw, ok := store.(switcher); ok {
		ctl.sw = sw
	}

	g := api.Group("/database")
	g.Get("", ctl.state)            // live database, health and what else is available
	g.Post("/test", ctl.test)       // probe a candidate without moving to it
	g.Post("/switch", ctl.switchTo) // move this instance to a database
	g.Post("/reset", ctl.reset)     // drop the saved selection, back to the environment
}
