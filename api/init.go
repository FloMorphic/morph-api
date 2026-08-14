// Package api wires the HTTP layer. RegisterAll mounts each entity's route
// group (one folder per entity, mirroring inspector-api) onto the Fiber app and
// injects the repository.Store the controllers persist through.
package api

import (
	contextControllers "github.com/FloMorphic/morph-api/api/context"
	extensionControllers "github.com/FloMorphic/morph-api/api/extension"
	hitlControllers "github.com/FloMorphic/morph-api/api/hitl"
	memoryControllers "github.com/FloMorphic/morph-api/api/memory"
	processControllers "github.com/FloMorphic/morph-api/api/process"
	promptControllers "github.com/FloMorphic/morph-api/api/prompt"
	settingsControllers "github.com/FloMorphic/morph-api/api/settings"
	triggerControllers "github.com/FloMorphic/morph-api/api/trigger"
	workflowControllers "github.com/FloMorphic/morph-api/api/workflow"
	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/mcpserver"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"
)

// RegisterAll mounts every route group. The store is threaded to each
// controller so nothing in the HTTP layer depends on a concrete database.
func RegisterAll(app fiber.Router, store repository.Store) {
	app.Use(recoverer.New(recoverer.Config{
		PanicHandler: func(c fiber.Ctx, r any) error {
			return c.Status(fiber.StatusInternalServerError).JSON(models.Response{Error: r})
		},
	}))

	// Liveness probe, intentionally before any auth gate.
	app.Get("/health", func(c fiber.Ctx) error {
		return etc.OK(c, fiber.Map{"status": "ok"})
	})

	// Engine event stream for the log drawer. Mounted before the auth gate so the
	// socket stays reachable even when the CRUD groups are guarded (the web app
	// mints no token in local mode).
	wslog.Register(app)

	// Public webhook ingress (/hooks/:slug). Mounted before the auth gate: its
	// callers are third parties authenticating with the hook's own credentials,
	// not an app bearer token.
	triggerControllers.RegisterIngress(app, store)

	// Optional HS256 bearer auth across the CRUD groups (off by default so the
	// web app works standalone).
	if env.AuthEnabled() {
		app.Use(etc.HS256SecKeyHandler())
	}

	workflowControllers.Register(app, store)
	contextControllers.Register(app, store)
	memoryControllers.Register(app, store)
	promptControllers.Register(app, store)
	hitlControllers.Register(app, store)
	settingsControllers.Register(app, store)
	processControllers.Register(app, store)
	extensionControllers.Register(app, store)
	triggerControllers.Register(app, store)

	// Embedded MCP server (/mcp). Mounted last, inside the guarded section, so it
	// inherits the same optional bearer gate as the CRUD groups. Off with
	// MCP_ENABLED=false. Its runtime-touching tools (start/stop a run, close a
	// human task) only work when the inflow runtime is connected, exactly like
	// the REST endpoints they mirror.
	if env.MCPEnabled() {
		mcpserver.Register(app, store)
	}
}
