// Package connectControllers is the HTTP controller for the Connect feature —
// the central OpenConnector integration. It has two halves:
//
//   - Connection management (/connect/connections): CRUD over the stored gateway
//     connections (hosted oomol or self-hosted OpenConnector), each holding a base
//     URL + bearer token, with one marked default. Tokens are stored so plugin
//     runs and the Connect page can reach the gateway; they are masked on read.
//   - Gateway proxy (/connect/gateway/*): an authenticated passthrough that
//     injects the resolved connection's token and forwards to the OpenConnector
//     REST surface, so the whole gateway API (providers, connections, OAuth,
//     action execution) is reachable without re-modelling each endpoint.
package connectControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.ConnectRepository
}

// Register mounts the /connect route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.Connect()}

	g := api.Group("/connect")

	// Connection management.
	g.Get("/connections", ctl.list)
	g.Post("/connections", ctl.upsert)                      // create (no id) or update
	g.Post("/connections/test", ctl.testInline)             // probe {baseUrl, token} before saving
	g.Get("/connections/id/:id", ctl.getByID)               // open one (masked token)
	g.Delete("/connections/id/:id", ctl.deleteByID)         // delete
	g.Post("/connections/id/:id/default", ctl.setDefault)   // mark default
	g.Post("/connections/id/:id/test", ctl.testStored)      // probe a stored connection

	// Authenticated passthrough to the OpenConnector gateway. Every method, any
	// sub-path — the connection is chosen by the `X-Connection-Id` header (or the
	// `__connection` query key), falling back to the default connection.
	g.All("/gateway/*", ctl.proxy)
}
