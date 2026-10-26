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

// savedFlow is the upsert response: the saved record (embedded, so the payload
// is the FlowRecord every client already reads) plus what compiling the graph
// found wrong with it.
type savedFlow struct {
	models.FlowRecord
	// Validation is the compile pass over the graph that was just saved — see
	// upsert for why it rides on the save response.
	Validation inflow.Diagnostics `json:"validation"`
}

// upsert handles POST /flow — create (no id) or update (existing id) a workflow.
//
// The saved graph is compiled on the way out and the result is returned with the
// record (`validation`). Saving is never gated on it: half-wired graphs are the
// normal state of a canvas being worked on, and a save that refused them would
// make the editor unusable. What it buys is TIMING — the compiled flow the engine
// runs is otherwise built on the wire, when a dispatched run asks for it, where
// a mistake is a log line nobody reads and a deadlocked join is just a run that
// hangs. Here the author gets the node, the edge and the suggested fix while
// they are still looking at the canvas. A launch does refuse (see
// inflow.StartWorkflow), so the two together mean "warned on save, stopped
// before a run".
//
// The saved record also comes back with its `plugins` list — the plugins behind
// the plugin-backed nodes the graph actually wires up, builtins tagged as such
// (see models.CollectFlowPlugins). It is derived by the save, so whatever a
// client sends under `plugins` is replaced by what the graph says.
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
	return etc.OK(c, savedFlow{
		FlowRecord: input,
		Validation: inflow.CompileFlow(input, inflow.WithGuard()).Diagnostics,
	})
}

// list handles GET /flow — page-based listing with total count.
//
// A row is a models.FlowSummary, NOT the saved record: the flow's Vue Flow
// graph is left out, and with it the bulk of the payload (a real flow's
// `view_flow` is tens of kilobytes, so a page of 20 was megabytes of JSON for a
// field no list draws — what made a consuming app's flow picker crawl). The
// node count and the flow's plugin list ride along for the badges a list shows,
// and the editor loads the graph of the one flow it opens with
// GET /flow/id/:id.
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
//
// `?validate=true` adds the graph validation (see upsert) to the answer, for a
// client re-checking a flow on demand rather than on its next save. It is off by
// default because this route's job is "what does this lower to", and because a
// guard finding is advice about one traversal — it never changes the status code
// here. Only a graph that did not lower at all is a 422, as it always was.
func (ctl *controller) compile(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	validate := c.Query("validate") == "true" || c.Query("validate") == "1"
	opts := []inflow.CompileOption{}
	if validate {
		opts = append(opts, inflow.WithGuard())
	}
	res := inflow.CompileFlow(*rec, opts...)
	if err := res.Diagnostics.CompileErr(); err != nil {
		// Nothing usable to return — the walk stopped at a node that would not
		// compile, so the graph is incomplete. The issues ride along as details.
		return etc.FailWith(c, fiber.StatusUnprocessableEntity, err.Error(),
			fiber.Map{"validation": res.Diagnostics})
	}
	out := fiber.Map{"startNodeId": res.StartNodeID, "nodes": res.Nodes}
	if validate {
		out["validation"] = res.Diagnostics
	}
	return etc.OK(c, out)
}

// deleteByID handles DELETE /flow/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "workflow not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}
