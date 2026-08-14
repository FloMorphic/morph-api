// Package designerControllers is the HTTP controller for the AI workflow-designer
// prompt. It exposes the central prompt builder (see the designer package) as an
// endpoint the web app's build-ai dialog calls, so the prompt the user copies
// into an assistant is authored in one place on the backend rather than in the
// front end. The imported-plugin actions listed in the prompt are resolved here
// from the extension table, the same rows the palette reads.
package designerControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	store repository.Store
}

// Register mounts the /designer route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{store: store}

	g := api.Group("/designer")
	g.Post("/prompt", ctl.prompt)
}
