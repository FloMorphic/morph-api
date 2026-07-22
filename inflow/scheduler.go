package inflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
)

// schedulerFallback is the scheduler's safety-net wake interval. The loop
// normally sleeps until exactly the nearest ScheduledAt (or a wake notify), so
// this only matters when a scheduled row is changed outside the normal path
// (e.g. a REST update rewriting scheduled_at) — it bounds how stale the armed
// timer can get. It is NOT a polling cadence; an idle scheduler doing a fallback
// pass runs one indexed LIMIT-1 query and goes back to sleep.
const schedulerFallback = 5 * time.Minute

// Scheduler launches `scheduled` process rows when their ScheduledAt arrives.
//
// Rather than ticking every minute, it treats the processes table as the
// priority queue: one goroutine reads the earliest scheduled row (indexed
// ORDER BY scheduled_at LIMIT 1) and sleeps in a timer until exactly that
// moment. The timer is re-armed on three signals — it fires (dispatch every due
// row, then re-arm on the new earliest), Notify() (a new scheduled row may now
// be the earliest), or the fallback tick. The first loop iteration runs
// immediately, so rows that came due while the app was down dispatch at startup.
type Scheduler struct {
	store repository.Store
	// wake is a 1-buffered coalescing signal: any number of Notify() calls while
	// the loop is busy collapse into a single re-arm pass.
	wake chan struct{}
}

// theScheduler is the process-wide instance StartWorkflow notifies when it
// records a scheduled row. Set once by StartScheduler before any handler runs.
var theScheduler *Scheduler

// StartScheduler creates the scheduler and starts its loop. Call it after the
// inflow backend is initialized (dispatch needs a resource candid to Exec on).
func StartScheduler(ctx context.Context, store repository.Store) *Scheduler {
	s := &Scheduler{store: store, wake: make(chan struct{}, 1)}
	theScheduler = s
	go s.run(ctx)
	fmt.Println("scheduler: started (timer-armed on nearest scheduled process)")
	return s
}

// Notify tells the scheduler the set of scheduled rows changed — a new row may
// be nearer than the one its timer is armed for. Never blocks.
func (s *Scheduler) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// notifyScheduler pokes the process-wide scheduler, if one is running.
func notifyScheduler() {
	if theScheduler != nil {
		theScheduler.Notify()
	}
}

// run is the scheduler loop: dispatch everything due, arm a timer for the
// nearest remaining row (capped by the fallback), sleep until the timer, a
// Notify, or ctx cancellation.
func (s *Scheduler) run(ctx context.Context) {
	for {
		s.dispatchDue(ctx)

		wait := schedulerFallback
		next, err := s.store.Processes().NextScheduled(ctx)
		switch {
		case err == nil:
			if d := time.Until(time.UnixMilli(next.ScheduledAt)); d < wait {
				wait = d
			}
			if wait <= 0 {
				// Became due between dispatch and here — loop right around.
				continue
			}
		case errors.Is(err, repository.ErrNotFound):
			// Nothing waiting; sleep until a Notify or the fallback tick.
		default:
			if ctx.Err() != nil {
				return
			}
			fmt.Printf("scheduler: read next scheduled: %v\n", err)
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// dispatchDue launches every scheduled row whose time has been reached. A row
// that fails to dispatch is marked failed (mirroring StartWorkflow's error
// path) so it is never picked up again.
func (s *Scheduler) dispatchDue(ctx context.Context) {
	due, err := s.store.Processes().ListDueScheduled(ctx, nowMillis())
	if err != nil {
		if ctx.Err() == nil {
			fmt.Printf("scheduler: list due processes: %v\n", err)
		}
		return
	}
	for i := range due {
		rec := &due[i]
		if err := s.launch(ctx, rec); err != nil {
			fmt.Printf("scheduler: launch process %d: %v\n", rec.IndexID, err)
			rec.Status = models.ProcessFailed
			rec.Error = err.Error()
			rec.FinishedAt = nowMillis()
			_ = s.store.Processes().Update(ctx, rec)
			continue
		}
		fmt.Printf("scheduler: launched %d (pid=%s flow=%s node=%s) scheduled for %d\n",
			rec.IndexID, rec.PID, rec.FlowID, rec.StartNodeID, rec.ScheduledAt)
	}
}

// launch dispatches one due scheduled row to the engine.
//
// The row is claimed (flipped to `running`) before the request is sent so a
// re-entrant dispatch pass can never double-fire it. The process request is
// rebuilt through fuse.NewProcess rather than replaying the stored snapshot —
// a fresh resource candid is picked at fire time (the one captured at schedule
// time may be gone by now) — but the stored PID is reused, so the engine's
// proc.finish event closes this same row via GetRunningByPID.
func (s *Scheduler) launch(ctx context.Context, rec *models.Process) error {
	rec.Status = models.ProcessRunning
	rec.StartedAt = nowMillis()
	if err := s.store.Processes().Update(ctx, rec); err != nil {
		return fmt.Errorf("claim row: %w", err)
	}

	// Carry the scheduled request's meta forward (it holds the indexId echo and
	// any backend-injected tags from schedule time).
	reqMeta := map[string]string{}
	if m, ok := rec.Request["meta"].(map[string]any); ok {
		for k, v := range m {
			if str, ok := v.(string); ok {
				reqMeta[k] = str
			}
		}
	}

	p, err := fuse.NewProcess(rec.StartNodeID,
		fuse.WithFlowId(rec.FlowID),
		fuse.WithContextDocument(rec.ContextID),
		fuse.WithMeta(reqMeta),
		fuse.WithPID(rec.PID),
	)
	if err != nil {
		return fmt.Errorf("build process request: %w", err)
	}
	rec.ResourceURL = p.GetResource()
	rec.Request = requestToMap(p.GetRequest())

	resp, err := p.Exec(ctx)
	if err != nil {
		return fmt.Errorf("dispatch process: %w", err)
	}
	// The engine echoes the pid; keep the row in sync if it differs.
	if resp != nil && resp.Data.PID != "" && resp.Data.PID != rec.PID {
		rec.PID = resp.Data.PID
	}
	if err := s.store.Processes().Update(ctx, rec); err != nil {
		return fmt.Errorf("record launch: %w", err)
	}
	return nil
}
