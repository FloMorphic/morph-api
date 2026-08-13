// Package triggerControllers is the HTTP controller for workflow triggers — the
// inbound webhooks and recurring schedules that launch a flow. It serves two
// surfaces: the authenticated CRUD group `/trigger` (managed from the Start
// node's drawer), and the PUBLIC ingress `/hooks/:slug`, which third parties
// call and which authenticates per-hook rather than through the app's auth gate.
package triggerControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	store repository.Store
}

// Register mounts the /trigger CRUD group. Register it WITH the other guarded
// groups (after the optional auth gate).
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{store: store}

	g := api.Group("/trigger")
	g.Post("", ctl.upsert)              // create / update
	g.Get("", ctl.list)                 // list (?flowId=&kind=&search=&page=&per_page=)
	g.Get("/id/:id", ctl.getByID)       // read one
	g.Delete("/id/:id", ctl.deleteByID) // delete
}

// RegisterIngress mounts the PUBLIC webhook endpoint. It MUST be registered
// before the app's auth gate — its callers are third parties carrying the hook's
// own credentials, not an app bearer token.
func RegisterIngress(api fiber.Router, store repository.Store) {
	ctl := &controller{store: store}
	api.All("/hooks/:slug", ctl.ingress)
}
