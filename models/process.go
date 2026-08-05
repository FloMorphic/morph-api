package models

// ProcessStatus is the lifecycle of a workflow run on the inflow engine.
//
//   - scheduled: recorded but not yet launched — a run that waits to reach its
//     ScheduledAt time before a request is sent to the engine.
//   - running:   a process request has been sent and the engine is executing it.
//   - waiting:   parked mid-run — e.g. a human-in-the-loop node finished this row
//     and the run will resume on a fresh row once the human answers.
//   - finished:  ran to completion (engine proc.finish, status "completed").
//   - stopped:   cancelled by a user or a timeout (engine proc.finish, "stopped").
//   - failed:    aborted on an error (engine proc.finish, "failed"); Error is set.
type ProcessStatus string

const (
	ProcessScheduled ProcessStatus = "scheduled"
	ProcessRunning   ProcessStatus = "running"
	ProcessWaiting   ProcessStatus = "waiting"
	ProcessFinished  ProcessStatus = "finished"
	ProcessStopped   ProcessStatus = "stopped"
	ProcessFailed    ProcessStatus = "failed"
)

// Process is one execution of a workflow on the inflow engine. It is created by
// the inflow layer when a process request is sent (never through a plain REST
// create) and closed out from the engine's `inflow.event.log` proc.finish event.
//
// IndexID is the *indexId*: an auto-increment integer that is the run's own
// identity, echoed into the ProcessRequest meta (and so shipped as a header on
// the services the run calls) so the run is addressable independently of PID.
// PID is the engine's process uuid and is intentionally NOT unique here — a
// single PID backs several rows over its lifetime (a human-in-the-loop pause
// finishes one row as `waiting`, and the human's answer starts a fresh row that
// resumes the same PID), which is exactly why resolution keys on IndexID, not PID.
//
// Request is the ProcessRequest snapshot sent to the engine. Meta is a free-form
// object the backend keeps alongside — most importantly the next-node list a
// human-in-the-loop (or other extrinsic) hands back when it parks a run, which
// the resume run is entered on directly: the engine takes a set of start nodes,
// so every branch the parked node fanned out to resumes as itself.
//
// StartNodeID is the first of those, not necessarily the only one — see
// inflow.MetaStartNodesKey for where the complete set is kept.
type Process struct {
	IndexID int64  `json:"indexId"`
	PID     string `json:"pid"`
	// InstanceID is the correlation id shared by every run of one logical workflow
	// instance — a run started from scratch and every run that resumes it after a
	// Human-in-the-Loop park carry the same InstanceID (the first run's pid),
	// while each keeps its own unique PID. It is what ties a park→resume chain
	// together for "latest status of this instance" reporting.
	InstanceID  string         `json:"instanceId,omitempty"`
	FlowID      string         `json:"flowId"`
	ContextID   string         `json:"contextId"`
	StartNodeID string         `json:"startNodeId"`
	Status      ProcessStatus  `json:"status"`
	ResourceURL string         `json:"resourceUrl,omitempty"`
	Request     map[string]any `json:"request,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
	Error       string         `json:"error,omitempty"`
	// ScheduledAt is the epoch-millis a scheduled run should launch at; 0 for an
	// immediate run.
	ScheduledAt int64 `json:"scheduledAt"`
	StartedAt   int64 `json:"startedAt"`
	FinishedAt  int64 `json:"finishedAt"`
	DurationMs  int64 `json:"durationMs"`
	CreatedAt   int64 `json:"createdAt"`
	UpdatedAt   int64 `json:"updatedAt"`
}
