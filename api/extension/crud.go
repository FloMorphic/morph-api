package extensionControllers

import (
	"encoding/json"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	InfraSpaces "github.com/Inflowenger/inflow-fusion/spaces"
	svcHandler "github.com/Inflowenger/inflow-fusion/svcHandler"
	"github.com/gofiber/fiber/v3"
)

// upsert handles POST /extension — create (no id) or update a palette node.
func (ctl *controller) upsert(c fiber.Ctx) error {
	var input models.ExtensionRecord
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid extension payload")
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "name is required")
	}
	if input.Kind == "" {
		input.Kind = models.KindExtension
	}
	if input.Kind != models.KindBuiltin && input.Kind != models.KindExtension {
		return etc.Fail(c, fiber.StatusBadRequest, "kind must be 'builtin' or 'extension'")
	}
	if err := ctl.repo.Upsert(c.Context(), &input); err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	return etc.OK(c, input)
}

// list handles GET /extension — page-based listing with total count. An optional
// `?kind=builtin|extension` scopes to one origin (the admin panel lists builtins,
// the extension portal lists extensions); the palette omits it to get both.
func (ctl *controller) list(c fiber.Ctx) error {
	var q models.PaginationParams
	if err := c.Bind().Query(&q); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	if err := q.Normalize(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	kind := strings.TrimSpace(c.Query("kind"))
	if kind != "" && kind != string(models.KindBuiltin) && kind != string(models.KindExtension) {
		return etc.Fail(c, fiber.StatusBadRequest, "kind must be 'builtin' or 'extension'")
	}
	items, total, err := ctl.repo.List(c.Context(), repository.ListParams{
		Offset: q.Offset(),
		Limit:  q.PerPage,
		Search: q.Search,
		Kind:   kind,
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "extensions not found")
	}
	return etc.OK(c, models.NewPage(items, total, q))
}

// getByID handles GET /extension/id/:id.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	return etc.OK(c, rec)
}

// deleteByID handles DELETE /extension/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}

// extrinsics handles GET /extension/extrinsics — the backend-registered extrinsic
// services (topicKey -> subject template) an extrinsic extension can bind to.
func (ctl *controller) extrinsics(c fiber.Ctx) error {
	return etc.OK(c, svcHandler.AllSvcSubjects())
}

// --- Live inflowv1 fetches (KindExtension only) ---------------------------
//
// These proxy the plugin over NATS through inflow-fusion. Nothing is stored: the
// intro/settings/actions/forms are always read live from the connected plugin.

func (ctl *controller) intro(c fiber.Ctx) error {
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginIntro(pluginID)
	})
}

func (ctl *controller) settings(c fiber.Ctx) error {
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginSettings(pluginID)
	})
}

func (ctl *controller) actions(c fiber.Ctx) error {
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginActions(pluginID)
	})
}

func (ctl *controller) actionForm(c fiber.Ctx) error {
	method := c.Params("method")
	return ctl.proxyByPlugin(c, func(pluginID string) ([]byte, error) {
		return InfraSpaces.DefaultPluginActionForm(pluginID, method)
	})
}

// proxyByPlugin resolves the extension's inflowv1 plugin id, runs the given fetch
// over NATS, and returns the plugin's raw JSON inside the standard envelope. It
// 400s for a non-extension row and 502s when the runtime/plugin is unreachable.
func (ctl *controller) proxyByPlugin(c fiber.Ctx, fetch func(pluginID string) ([]byte, error)) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "extension not found")
	}
	if strings.TrimSpace(rec.PluginID) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "extension has no plugin id (not an inflowv1 plugin)")
	}
	raw, err := fetch(rec.PluginID)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	return etc.OK(c, json.RawMessage(raw))
}
