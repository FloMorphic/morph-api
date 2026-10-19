package workflowControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

// upsert handles POST /flow — create (no id) or update (existing id) a workflow.
func (ctl *controller) upsert(c fiber.Ctx) error {
	var input models.FlowRecord
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid workflow payload")
	}
	if strings.TrimSpace(input.Title) == "" {
		input.Title = "untitled workflow"
	}
	// Fill headless-author graph defaults on the one shared path. A no-op for a
	// graph the editor produced (see inflow.NormalizeGraph), so this changes
	// nothing for the visual builder while keeping it in lockstep with MCP.
	inflow.NormalizeGraph(&input)
	if err := ctl.repo.Upsert(c.Context(), &input); err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	// Live sync: tell any open editor the flow changed so it can refetch without
	// a manual refresh (a no-op when no client is connected).
	wslog.Emit("flow.changed", fiber.Map{"id": input.ID, "source": "api"})
	return etc.OK(c, input)
}

// list handles GET /flow — page-based listing with total count.
//
// `tags` is an optional comma-separated match-ANY label filter: a flow is kept
// when it carries at least one of them. It is what lets a consumer of this
// install show the operator the handful of flows one compliance framework
// installed instead of every flow on the canvas — and an absent or empty
// `tags` is the unfiltered list, so clearing the filter needs no second route.
func (ctl *controller) list(c fiber.Ctx) error {
	var q models.PaginationParams
	if err := c.Bind().Query(&q); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	if err := q.Normalize(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	items, total, err := ctl.repo.List(c.Context(), repository.ListParams{
		Offset: q.Offset(),
		Limit:  q.PerPage,
		Search: q.Search,
		Tags:   splitTags(c.Query("tags")),
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "workflows not found")
	}
	return etc.OK(c, models.NewPage(items, total, q))
}

// splitTags reads a comma-separated `tags` query parameter. Normalization
// (lower-casing, de-duplication) is the repository's job, so "GDPR, iso27001"
// and "gdpr,iso27001" filter alike.
func splitTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// setTagsInput is the body of PUT /flow/id/:id/tags. `mode` is "set" (the
// default — the list becomes exactly these) or "merge" (add these, keep the
// rest). An empty list with mode "set" clears the flow's tags.
type setTagsInput struct {
	Tags []string `json:"tags"`
	Mode string   `json:"mode"`
}

// setTags handles PUT /flow/id/:id/tags — relabel a saved flow without
// round-tripping its graph.
//
// A flow's tags are what a consumer filters the flow list by, and they are set
// from two sides: the author on the canvas, and whatever installed the flow
// (SurfStat stamping the framework a flow came with). The second has no
// business reading, re-sending and possibly stale-overwriting a 35 KB
// view_flow just to add a label, which is why this is its own route.
func (ctl *controller) setTags(c fiber.Ctx) error {
	var in setTagsInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid tags payload")
	}
	switch in.Mode {
	case "", "set", "merge":
	default:
		return etc.Fail(c, fiber.StatusBadRequest, `mode must be "set" or "merge"`)
	}
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	if in.Mode == "merge" {
		rec.Tags = append(append([]string{}, rec.Tags...), in.Tags...)
	} else {
		rec.Tags = in.Tags
	}
	if err := ctl.repo.Upsert(c.Context(), rec); err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	wslog.Emit("flow.changed", fiber.Map{"id": rec.ID, "source": "api"})
	return etc.OK(c, fiber.Map{"id": rec.ID, "tags": rec.Tags})
}

// getByID handles GET /flow/id/:id.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	return etc.OK(c, rec)
}

// compile handles GET /flow/id/:id/compile — run the inflow compiler over a
// saved workflow and return the lowered node graph, without launching a run.
//
// This is a debug / introspection aid: it surfaces exactly what a Vue Flow graph
// lowers to on the engine (every morphic node's inflow primitive, its wiring and
// the edge-derived post-pass), which is otherwise only visible from inside a
// running process. It never touches the engine and creates nothing.
func (ctl *controller) compile(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	startNodeID, nodes, err := inflow.FLowCompiler(*rec)
	if err != nil {
		return etc.Fail(c, fiber.StatusUnprocessableEntity, err.Error())
	}
	return etc.OK(c, fiber.Map{"startNodeId": startNodeID, "nodes": nodes})
}

// deleteByID handles DELETE /flow/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}
