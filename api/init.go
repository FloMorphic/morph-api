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
	workflowControllers "github.com/FloMorphic/morph-api/api/workflow"
	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/env"
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
}
