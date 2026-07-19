// Package api wires the HTTP layer. RegisterAll mounts each entity's route
// group (one folder per entity, mirroring inspector-api) onto the Fiber app and
// injects the repository.Store the controllers persist through.
package api

import (
	contextControllers "github.com/FloMorphic/morph-api/api/context"
	memoryControllers "github.com/FloMorphic/morph-api/api/memory"
	promptControllers "github.com/FloMorphic/morph-api/api/prompt"
	workflowControllers "github.com/FloMorphic/morph-api/api/workflow"
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

	// Optional HS256 bearer auth across the CRUD groups (off by default so the
	// web app works standalone).
	if env.AuthEnabled() {
		app.Use(etc.HS256SecKeyHandler())
	}

	workflowControllers.Register(app, store)
	contextControllers.Register(app, store)
	memoryControllers.Register(app, store)
	promptControllers.Register(app, store)
}
