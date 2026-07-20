package settingsControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

// upsert handles POST /settings — create (no id) or update a settings profile.
func (ctl *controller) upsert(c fiber.Ctx) error {
	var input models.NodeSetting
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid settings payload")
	}
	input.NodeUniqID = strings.TrimSpace(input.NodeUniqID)
	if input.NodeUniqID == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "nodeUniqId is required")
	}
	if strings.TrimSpace(input.Title) == "" {
		input.Title = "default"
	}
	if input.Settings == nil {
		input.Settings = map[string]any{}
	}
	if err := ctl.repo.Upsert(c.Context(), &input); err != nil {
		return etc.FailFromRepo(c, err, "settings profile not found")
	}
	return etc.OK(c, input)
}

// list handles GET /settings — page-based listing with total count. An optional
// `?node=<nodeUniqId>` scopes the result to one node's profiles (used by the
// node drawer's profile selector).
func (ctl *controller) list(c fiber.Ctx) error {
	var q models.PaginationParams
	if err := c.Bind().Query(&q); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	if err := q.Normalize(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	items, total, err := ctl.repo.List(c.Context(), repository.ListParams{
		Offset:     q.Offset(),
		Limit:      q.PerPage,
		Search:     q.Search,
		NodeUniqID: strings.TrimSpace(c.Query("node")),
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "settings profiles not found")
	}
	return etc.OK(c, models.NewPage(items, total, q))
}

// getByID handles GET /settings/id/:id.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "settings profile not found")
	}
	return etc.OK(c, rec)
}

// deleteByID handles DELETE /settings/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "settings profile not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}
