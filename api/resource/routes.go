// Package resourceControllers is the HTTP controller for the inflow engine
// dispatch pool — the set of engine instances a workflow run can be sent to.
//
// The pool itself lives in the inflow-fusion SDK (loaded from infra at startup
// and kept in a round-robin). These endpoints expose it to the flomorphic-wapp
// settings dialog: list the live candidates, add one by hand, and pin all
// dispatch to a single resource (the "use just this one" switch) or release it
// back to round-robin. Nothing is persisted here — the pool is runtime state
// owned by the SDK, so a reload re-reads it from infra.
package resourceControllers

import (
	"github.com/gofiber/fiber/v3"
)

// Register mounts the /resource route group.
func Register(api fiber.Router) {
	ctl := &controller{}

	g := api.Group("/resource")
	g.Get("", ctl.list)             // list live dispatch candidates
	g.Post("", ctl.add)             // add a resource by hand
	g.Post("/pin", ctl.pin)         // pin all dispatch to one resource
	g.Post("/unpin", ctl.unpin)     // release the pin, back to round-robin
	g.Post("/reload", ctl.reload)   // re-read the pool from infra
}
