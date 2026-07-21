package inflow

import (
	"context"
	"fmt"
	"strconv"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	"github.com/Inflowenger/inflow-fusion/logs"
	"github.com/gofiber/contrib/v3/socketio"
	"github.com/nats-io/nats.go"
)

// SubscribeProcessEvents subscribes to the engine's event log and closes out
// process rows when a run finishes.
//
// The event log is a shared, fire-and-forget stream carrying every kind of event
// for every process on the engine, so the handler cheaply drops anything that is
// not a v1 proc.finish. proc.finish carries only the pid (no meta), so a row is
// resolved by the single `running` row for that pid — which is exactly why the
// record keeps an indexId of its own: a pid can back several rows, but only one
// is ever `running`. As a defensive fast path we honour an `indexId` header when
// the runtime supplies one.
func SubscribeProcessEvents(store repository.Store) error {
	backend := fuse.GetInflowBackend()
	if backend == nil {
		return fmt.Errorf("inflow backend not initialised")
	}
	con, err := backend.GetInflowEventsPipe()
	if err != nil {
		return fmt.Errorf("open inflow events pipe: %w", err)
	}
	if _, err := con.Subscribe(logs.DefaultSubjectEventLog, func(msg *nats.Msg) {
		// Relay every raw event to the log-drawer sockets before doing anything
		// with it — the frontend's flow-trace tracker decides what is usable, so
		// the API stays a verbatim pipe (cf. inspector-api). A broadcast with no
		// connected clients is a no-op.
		socketio.Broadcast(msg.Data, socketio.TextMessage)
		handleProcFinish(store, msg)
	}); err != nil {
		return fmt.Errorf("subscribe %s: %w", logs.DefaultSubjectEventLog, err)
	}
	fmt.Println("process events subscription registered on ", logs.DefaultSubjectEventLog)
	return nil
}

func handleProcFinish(store repository.Store, msg *nats.Msg) {
	f, ok := logs.CaptureProcFinish(msg.Data)
	if !ok {
		return // not a proc.finish event — normal on a shared subject.
	}

	rec := resolveFinishedRecord(store, msg, f.Pid)
	if rec == nil {
		fmt.Printf("process: proc.finish for pid=%s has no running row to close\n", f.Pid)
		return
	}

	rec.Status = finishStatus(f.Status)
	rec.FinishedAt = f.Ts
	if rec.FinishedAt == 0 {
		rec.FinishedAt = nowMillis()
	}
	rec.DurationMs = f.DurationMs
	rec.Error = f.Error
	if err := store.Processes().Update(context.Background(), rec); err != nil {
		fmt.Printf("process: failed to close row %d: %v\n", rec.IndexID, err)
		return
	}
	fmt.Printf("process: closed %d (pid=%s status=%s duration=%dms)\n", rec.IndexID, rec.PID, rec.Status, rec.DurationMs)
}

// resolveFinishedRecord finds the row a proc.finish event closes: an explicit
// `indexId` header first, else the running row for the pid.
func resolveFinishedRecord(store repository.Store, msg *nats.Msg, pid string) *models.Process {
	if msg.Header != nil {
		if raw := msg.Header.Get(MetaIndexKey); raw != "" {
			if idx, err := strconv.ParseInt(raw, 10, 64); err == nil {
				if rec, err := store.Processes().GetByIndex(context.Background(), idx); err == nil {
					return rec
				}
			}
		}
	}
	rec, err := store.Processes().GetRunningByPID(context.Background(), pid)
	if err != nil {
		return nil
	}
	return rec
}

// finishStatus maps an engine finish status to our lifecycle status.
func finishStatus(status string) models.ProcessStatus {
	switch status {
	case logs.FinishCompleted:
		return models.ProcessFinished
	case logs.FinishStopped:
		return models.ProcessStopped
	case logs.FinishFailed:
		return models.ProcessFailed
	default:
		return models.ProcessFinished
	}
}
