package processControllers

import (
	"strconv"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

// startInput is the body of POST /process — launch a workflow run.
//
// The request meta shipped to the engine is NOT part of this body: it is
// assembled in the backend (its indexId is only known after the row is
// inserted, see inflow.StartWorkflow). The frontend supplies only what
// identifies the run and any backend-only record meta.
type startInput struct {
	FlowID      string         `json:"flowId"`
	ContextID   string         `json:"contextId"`
	StartNodeID string         `json:"startNodeId"`
	Meta        map[string]any `json:"meta"`
	ScheduledAt int64          `json:"scheduledAt"`
}

// start handles POST /process — record a process row and dispatch the run to the
// inflow engine (or record it as `scheduled` when scheduledAt is in the future).
func (ctl *controller) start(c fiber.Ctx) error {
	var in startInput
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid process payload")
	}
	if strings.TrimSpace(in.FlowID) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "flowId is required")
	}
	if strings.TrimSpace(in.ContextID) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "contextId is required")
	}
	rec, err := inflow.StartWorkflow(c.Context(), ctl.store, inflow.StartParams{
		FlowID:      in.FlowID,
		ContextID:   in.ContextID,
		StartNodeID: in.StartNodeID,
		RecordMeta:  in.Meta,
		ScheduledAt: in.ScheduledAt,
		// ReqMeta is intentionally not taken from the request body — the backend
		// assembles the engine meta (indexId + any controller-supplied extras).
	})
	if err != nil {
		// A launch failure still leaves a `failed` row (rec) when one was
		// recorded; report the error but surface the row when we have it.
		if rec != nil {
			return etc.Send(c, fiber.StatusBadGateway, rec, err.Error())
		}
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	return etc.OK(c, rec)
}

// stop handles POST /process/id/:id/stop — ask the engine to stop the run and
// mark the row `stopped`.
func (ctl *controller) stop(c fiber.Ctx) error {
	idx, ok := parseIndex(c)
	if !ok {
		return etc.Fail(c, fiber.StatusBadRequest, "indexId must be an integer")
	}
	rec, err := inflow.StopWorkflow(c.Context(), ctl.store, idx)
	if err != nil {
		if repository.IsNotFound(err) {
			return etc.Fail(c, fiber.StatusNotFound, "process not found")
		}
		if rec != nil {
			return etc.Send(c, fiber.StatusBadGateway, rec, err.Error())
		}
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	return etc.OK(c, rec)
}

// list handles GET /process — page-based listing with optional status and pid
// filters plus an id/pid/flow search.
func (ctl *controller) list(c fiber.Ctx) error {
	var q models.PaginationParams
	if err := c.Bind().Query(&q); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	if err := q.Normalize(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	status := strings.TrimSpace(c.Query("status"))
	if !validStatusFilter(status) {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid status filter")
	}
	items, total, err := ctl.store.Processes().List(c.Context(), repository.ListParams{
		Offset: q.Offset(),
		Limit:  q.PerPage,
		Search: q.Search,
		Status: status,
		PID:    strings.TrimSpace(c.Query("pid")),
		FlowID: strings.TrimSpace(c.Query("flowId")),
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "processes not found")
	}
	return etc.OK(c, models.NewPage(items, total, q))
}

// getByID handles GET /process/id/:id, where :id is the integer indexId.
func (ctl *controller) getByID(c fiber.Ctx) error {
	idx, ok := parseIndex(c)
	if !ok {
		return etc.Fail(c, fiber.StatusBadRequest, "indexId must be an integer")
	}
	rec, err := ctl.store.Processes().GetByIndex(c.Context(), idx)
	if err != nil {
		return etc.FailFromRepo(c, err, "process not found")
	}
	return etc.OK(c, rec)
}

// deleteByID handles DELETE /process/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	idx, ok := parseIndex(c)
	if !ok {
		return etc.Fail(c, fiber.StatusBadRequest, "indexId must be an integer")
	}
	if err := ctl.store.Processes().Delete(c.Context(), idx); err != nil {
		return etc.FailFromRepo(c, err, "process not found")
	}
	return etc.OK(c, fiber.Map{"deleted": true})
}

// parseIndex reads the integer indexId from the :id path param, returning ok
// false when it is not an integer.
func parseIndex(c fiber.Ctx) (int64, bool) {
	idx, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// validStatusFilter allows an empty filter or one of the known lifecycle states.
func validStatusFilter(status string) bool {
	switch models.ProcessStatus(status) {
	case "",
		models.ProcessScheduled,
		models.ProcessRunning,
		models.ProcessWaiting,
		models.ProcessFinished,
		models.ProcessStopped,
		models.ProcessFailed:
		return true
	default:
		return false
	}
}
