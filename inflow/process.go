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
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// MetaIndexKey is the ProcessRequest meta key the run's indexId is echoed into.
// It travels with the request (and, per the runtime, is shipped as a header on
// the services a run calls), so a run is addressable by its own record id
// independently of the engine's pid.
const MetaIndexKey = "indexId"

// MetaFlowKey / MetaContextKey carry the run's flow and context ids in the
// request meta. The runtime ships every meta entry as a header on the services a
// run calls, but — unlike the pid — it does not put flowId/contextId on those
// headers on its own. A svc handler that has to reach back to the run (the hitl
// handler recording a task it will later resume; continue-at) reads them from
// the header, so we place them in the meta here to guarantee they arrive.
const (
	MetaFlowKey    = "flowId"
	MetaContextKey = "contextId"
	// MetaPidKey carries the run's pid. Every run gets its own pid (the engine
	// mints one when none is given, and two runs must never share a pid — the log
	// stream demuxes events by it). We mint it up front instead so it can travel
	// in the meta as a header: without that a svc handler cannot learn the pid of
	// the run calling it, which is what left HITL tasks with an empty pid.
	MetaPidKey = "pid"
	// MetaInstanceKey carries the run's workflow-instance correlation id (see
	// models.Process.InstanceID). It rides in the meta as a header so a svc handler
	// — the hitl one recording a task — knows which logical instance it is part of,
	// and a resume can continue that same instance.
	MetaInstanceKey = "instanceId"
)

// MetaStartNodesKey is the RecordMeta key holding the run's full start-node set.
// The process row's StartNodeID column carries only the first one (it is a
// single column, and one node is all an ordinary run has), so a run started on
// several nodes — a resume that picks up every branch a parked node fanned out
// to — keeps the complete set here, which is what the scheduler relaunches from.
const MetaStartNodesKey = "startNodeIds"

// MetaResumeKey is the RecordMeta flag marking a run as a continuation of an
// earlier one over the same context (a HITL resume or a Continue After). It is
// persisted on the row because a scheduled row is re-dispatched by the scheduler
// through a freshly built request, not by replaying the stored one, so the
// resume intent has to survive on the record to reach that later launch.
const MetaResumeKey = "resume"

// StartParams describes a workflow run to launch.
type StartParams struct {
	// FlowID is the workflow to run (required).
	FlowID string
	// ContextID is the context document the run reads/writes (required).
	ContextID string
	// StartNodeIDs overrides the flow's start node. Empty resolves the flow's
	// `startNode`. A resume run passes every next node the parked node fanned
	// out to — the engine accepts a set of entry points and runs them all, so a
	// multi-branch park resumes as the branches it actually had.
	StartNodeIDs []string
	// InstanceID ties this run to a logical workflow instance. Empty starts a new
	// instance (the run's own freshly minted pid becomes the InstanceID); a resume
	// after a HITL park passes the parked task's InstanceID so the whole chain
	// shares one correlation id while each run keeps its own pid.
	InstanceID string
	// ReqMeta is extra ProcessRequest meta shipped to the engine (string map).
	// It is a backend/controller injection point (e.g. an account tag) — never
	// populated from a frontend request body — because the meta is assembled
	// server-side. indexId is added automatically; do not set it here.
	ReqMeta map[string]string
	// RecordMeta is backend-only meta kept on the process row — most importantly
	// the next-node list handed back when a run parks (human-in-the-loop), which
	// a resume run is entered on. Never sent to the engine.
	RecordMeta map[string]any
	// ScheduledAt, when non-zero (epoch millis), records the run as `scheduled`
	// and does NOT dispatch it — a scheduler launches it when the time is reached.
	ScheduledAt int64
	// Resume marks this run as a continuation of an earlier run over the same
	// context (a HITL resume or a Continue After): the engine seeds the traversal
	// snapshot the earlier run left in the context header, so a join downstream of
	// the resume point sees its already-completed dependencies instead of locking.
	// A fresh run leaves it false. Persisted on the row (MetaResumeKey) so a
	// scheduled resume still carries the flag when the scheduler launches it.
	Resume bool
	// Settings overrides the engine run settings (proc timeout, node-traversal
	// limit, fallback request timeout). Zero fields keep the fusion defaults, so a
	// caller that only wants to bump one leaves the others at 0. See RunSettings.
	Settings RunSettings
}

// RunSettings carries the caller-tunable engine settings for one run. Each is an
// override of the inflow-fusion default (proc_timeout 1h, proc_node_limit 500,
// svc_req_timeout 5s); a zero value means "leave the default", so a partially
// filled struct only moves the fields it sets.
type RunSettings struct {
	// ExecuteTimeoutSec caps how long the whole run may take, in seconds.
	ExecuteTimeoutSec int64
	// ProcessNodeLimit stops a run after this many node visits — the guard against
	// runaway loops. Capped to uint16 by the engine's Settings type.
	ProcessNodeLimit uint16
	// RequestTimeoutSec is the fallback per-request timeout (seconds) used for any
	// http/nats call that did not set its own.
	RequestTimeoutSec int64
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

	// Resolve the start node from the flow when the caller did not pin any.
	startNodeIDs := nonEmpty(params.StartNodeIDs)
	if len(startNodeIDs) == 0 {
		rec, err := store.Workflows().GetByID(ctx, params.FlowID)
		if err != nil {
			return nil, fmt.Errorf("load flow %s: %w", params.FlowID, err)
		}
		startNodeID, err := GetStartNodeId(*rec)
		if err != nil {
			return nil, err
		}
		startNodeIDs = []string{startNodeID}
	}

	// The row's column holds one node; the full set travels on the record meta so
	// a scheduled row can be relaunched on all of them (see MetaStartNodesKey).
	recordMeta := map[string]any{}
	for k, v := range params.RecordMeta {
		recordMeta[k] = v
	}
	recordMeta[MetaStartNodesKey] = startNodeIDs
	// Persist the resume intent so a scheduled row (relaunched through a freshly
	// built request) still resumes; only when set, to keep the meta clean.
	if params.Resume {
		recordMeta[MetaResumeKey] = true
	}

	// Create the row first so the auto-increment index_id is assigned; it is the
	// indexId echoed into request meta below.
	now := nowMillis()
	rec := &models.Process{
		FlowID:      params.FlowID,
		ContextID:   params.ContextID,
		StartNodeID: startNodeIDs[0],
		Meta:        recordMeta,
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
	// So svc handlers can reach back to this run (see the Meta*Key doc). Not
	// overridden if a caller already supplied them.
	if _, ok := reqMeta[MetaFlowKey]; !ok {
		reqMeta[MetaFlowKey] = params.FlowID
	}
	if _, ok := reqMeta[MetaContextKey]; !ok {
		reqMeta[MetaContextKey] = params.ContextID
	}
	// Mint this run's pid ourselves so it is known before dispatch: it goes to the
	// engine via WithPID and travels in the meta as a header, so a svc handler
	// (the hitl one, recording a task) learns which run called it. This is a fresh
	// pid even for a hitl resume — that run is a new run entered on the parked
	// node's next edges, linked to its origin through the task's meta, not by
	// sharing a pid (which the event stream could not tell apart).
	pid := uuid.NewString()
	reqMeta[MetaPidKey] = pid
	// A new instance starts under its own pid; a resume passes the parked run's
	// InstanceID so the whole park→resume chain shares one correlation id.
	instanceID := strings.TrimSpace(params.InstanceID)
	if instanceID == "" {
		instanceID = pid
	}
	reqMeta[MetaInstanceKey] = instanceID
	rec.InstanceID = instanceID

	opts := []func(*fuse.Process){
		fuse.WithFlowId(params.FlowID),
		fuse.WithContextDocument(params.ContextID),
		fuse.WithMeta(reqMeta),
		fuse.WithPID(pid),
	}
	// A continuation seeds the earlier run's traversal snapshot in the engine.
	if params.Resume {
		opts = append(opts, fuse.WithResume())
	}
	// Apply only the settings the caller set; a zero field keeps the fusion default.
	if params.Settings.ExecuteTimeoutSec > 0 {
		opts = append(opts, fuse.WithProcessTimeout(time.Duration(params.Settings.ExecuteTimeoutSec)*time.Second))
	}
	if params.Settings.RequestTimeoutSec > 0 {
		opts = append(opts, fuse.WithRequestTimeout(time.Duration(params.Settings.RequestTimeoutSec)*time.Second))
	}
	if params.Settings.ProcessNodeLimit > 0 {
		opts = append(opts, fuse.WithNodeLimit(params.Settings.ProcessNodeLimit))
	}

	p, err := fuse.NewProcess(startNodeIDs, opts...)
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
	fmt.Printf("process: launched %d (pid=%s flow=%s nodes=%v)\n", rec.IndexID, rec.PID, rec.FlowID, startNodeIDs)
	return rec, nil
}

// nonEmpty returns the blank-free subset of ids, preserving order. A caller that
// builds its start set from captured edges can hand over whatever it found
// without pre-filtering.
func nonEmpty(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, id)
		}
	}
	return out
}

// nextNodeIDs is the node id of every outbound edge a parked node handed back,
// in edge order — the entry set a resume run is launched on.
func nextNodeIDs(nexts []inflowModels.Next) []string {
	ids := make([]string, 0, len(nexts))
	for _, n := range nexts {
		ids = append(ids, n.NodeId)
	}
	return nonEmpty(ids)
}

// startNodesFor recovers a recorded run's full start-node set: the meta list
// when one was kept, otherwise the row's single column (any row written before
// multi-node starts existed).
// resumeFor reports whether a scheduled row is a continuation, read from the
// record meta the way startNodesFor reads its start set — so the scheduler,
// which rebuilds the request rather than replaying it, still resumes.
func resumeFor(rec *models.Process) bool {
	if raw, ok := rec.Meta[MetaResumeKey]; ok {
		if b, ok := raw.(bool); ok {
			return b
		}
	}
	return false
}

func startNodesFor(rec *models.Process) []string {
	if raw, ok := rec.Meta[MetaStartNodesKey]; ok {
		if list, ok := raw.([]any); ok {
			ids := make([]string, 0, len(list))
			for _, v := range list {
				if s, ok := v.(string); ok {
					ids = append(ids, s)
				}
			}
			if ids = nonEmpty(ids); len(ids) > 0 {
				return ids
			}
		}
		if list, ok := raw.([]string); ok {
			if ids := nonEmpty(list); len(ids) > 0 {
				return ids
			}
		}
	}
	return nonEmpty([]string{rec.StartNodeID})
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
