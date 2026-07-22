package inflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	"github.com/bytedance/sonic"
)

// MetaIndexKey is the ProcessRequest meta key the run's indexId is echoed into.
// It travels with the request (and, per the runtime, is shipped as a header on
// the services a run calls), so a run is addressable by its own record id
// independently of the engine's pid.
const MetaIndexKey = "indexId"

// StartParams describes a workflow run to launch.
type StartParams struct {
	// FlowID is the workflow to run (required).
	FlowID string
	// ContextID is the context document the run reads/writes (required).
	ContextID string
	// StartNodeID overrides the flow's start node. Empty resolves the flow's
	// `startNode`; a resume run sets it to a virtual start node whose outbounds
	// are the parked next nodes (see RecordMeta).
	StartNodeID string
	// ReqMeta is extra ProcessRequest meta shipped to the engine (string map).
	// It is a backend/controller injection point (e.g. an account tag) — never
	// populated from a frontend request body — because the meta is assembled
	// server-side. indexId is added automatically; do not set it here.
	ReqMeta map[string]string
	// RecordMeta is backend-only meta kept on the process row — most importantly
	// the next-node list handed back when a run parks (human-in-the-loop), which
	// a resume run rebuilds its virtual start node from. Never sent to the engine.
	RecordMeta map[string]any
	// ScheduledAt, when non-zero (epoch millis), records the run as `scheduled`
	// and does NOT dispatch it — a scheduler launches it when the time is reached.
	ScheduledAt int64
}

// StartWorkflow records a process row and (unless scheduled) dispatches the
// process request to the inflow engine.
//
// Because the indexId is an auto-increment integer, the row is Created first to
// learn it, that index is echoed into the ProcessRequest meta (so the run
// carries its own record identity into every service it calls), and the row is
// then Updated with the built request before Exec — so a proc.finish that races
// back on `inflow.event.log` always finds a row to close.
func StartWorkflow(ctx context.Context, store repository.Store, params StartParams) (*models.Process, error) {
	if strings.TrimSpace(params.FlowID) == "" {
		return nil, fmt.Errorf("flowId is required")
	}
	if strings.TrimSpace(params.ContextID) == "" {
		return nil, fmt.Errorf("contextId is required")
	}

	// Resolve the start node from the flow when the caller did not pin one.
	startNodeID := params.StartNodeID
	if strings.TrimSpace(startNodeID) == "" {
		rec, err := store.Workflows().GetByID(ctx, params.FlowID)
		if err != nil {
			return nil, fmt.Errorf("load flow %s: %w", params.FlowID, err)
		}
		startNodeID, err = GetStartNodeId(*rec)
		if err != nil {
			return nil, err
		}
	}

	// Create the row first so the auto-increment index_id is assigned; it is the
	// indexId echoed into request meta below.
	now := nowMillis()
	rec := &models.Process{
		FlowID:      params.FlowID,
		ContextID:   params.ContextID,
		StartNodeID: startNodeID,
		Meta:        params.RecordMeta,
		ScheduledAt: params.ScheduledAt,
		Status:      models.ProcessRunning,
		StartedAt:   now,
	}
	if params.ScheduledAt > 0 {
		rec.Status = models.ProcessScheduled
		rec.StartedAt = 0
	}
	if err := store.Processes().Create(ctx, rec); err != nil {
		return nil, fmt.Errorf("record process: %w", err)
	}

	// Echo the assigned index into the request meta, then build the request.
	reqMeta := map[string]string{}
	for k, v := range params.ReqMeta {
		reqMeta[k] = v
	}
	reqMeta[MetaIndexKey] = strconv.FormatInt(rec.IndexID, 10)

	p, err := fuse.NewProcess(startNodeID,
		fuse.WithFlowId(params.FlowID),
		fuse.WithContextDocument(params.ContextID),
		fuse.WithMeta(reqMeta),
	)
	if err != nil {
		rec.Status = models.ProcessFailed
		rec.Error = err.Error()
		rec.FinishedAt = nowMillis()
		_ = store.Processes().Update(ctx, rec)
		return rec, fmt.Errorf("build process request: %w", err)
	}
	req := p.GetRequest()
	rec.PID = req.PID
	rec.ResourceURL = p.GetResource()
	rec.Request = requestToMap(req)
	if err := store.Processes().Update(ctx, rec); err != nil {
		return rec, fmt.Errorf("record process request: %w", err)
	}

	if params.ScheduledAt > 0 {
		// Recorded, not dispatched — wake the scheduler so it re-arms its timer
		// in case this row is now the nearest one.
		notifyScheduler()
		return rec, nil
	}

	resp, err := p.Exec(ctx)
	if err != nil {
		rec.Status = models.ProcessFailed
		rec.Error = err.Error()
		rec.FinishedAt = nowMillis()
		_ = store.Processes().Update(ctx, rec)
		return rec, fmt.Errorf("dispatch process: %w", err)
	}
	// The engine echoes the pid; keep the row in sync if it differs.
	if resp != nil && resp.Data.PID != "" && resp.Data.PID != rec.PID {
		rec.PID = resp.Data.PID
		_ = store.Processes().Update(ctx, rec)
	}
	fmt.Printf("process: launched %d (pid=%s flow=%s node=%s)\n", rec.IndexID, rec.PID, rec.FlowID, rec.StartNodeID)
	return rec, nil
}

// StopWorkflow asks the engine to stop the run behind a process row (by its
// indexId) and marks the row `stopped`. The row keeps the pid and resource url
// captured at launch, so no extra input is needed (cf. inspector-api's stopByPid
// which takes them in the request body).
func StopWorkflow(ctx context.Context, store repository.Store, indexID int64) (*models.Process, error) {
	rec, err := store.Processes().GetByIndex(ctx, indexID)
	if err != nil {
		return nil, err
	}
	if rec.PID == "" {
		return rec, fmt.Errorf("process %d has no pid to stop", indexID)
	}
	if _, err := fuse.StopProcess(ctx, rec.PID, rec.ResourceURL); err != nil {
		return rec, fmt.Errorf("stop process %d (pid=%s): %w", indexID, rec.PID, err)
	}
	rec.Status = models.ProcessStopped
	rec.FinishedAt = nowMillis()
	if err := store.Processes().Update(ctx, rec); err != nil {
		return rec, fmt.Errorf("record stop: %w", err)
	}
	return rec, nil
}

// requestToMap renders the ProcessRequest to a generic object for storage. It
// never fails in practice; a marshal error yields an empty snapshot rather than
// blocking the run.
func requestToMap(req any) map[string]any {
	b, err := sonic.Marshal(req)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := sonic.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

func nowMillis() int64 { return time.Now().UnixMilli() }
