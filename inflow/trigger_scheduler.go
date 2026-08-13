package inflow

import (
	"context"
	"fmt"
	"time"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

// triggerSchedulerFallback bounds how long the recurring scheduler sleeps when
// nothing is nearer — a safety net for schedule rows changed outside the normal
// Notify path. It is not a polling cadence: an idle scheduler wakes, runs one
// indexed list, and sleeps again.
const triggerSchedulerFallback = 5 * time.Minute

// TriggerScheduler fires enabled schedule triggers when their next occurrence
// arrives. Unlike inflow.Scheduler (which dispatches one-shot `scheduled` process
// rows for the Continue After node), this arms recurring cron/interval triggers
// and starts a FRESH run each time one comes due, then re-arms.
//
// It treats every schedule as a robfig cron.Schedule (an interval is normalized
// to an `@every` spec upstream, so there is a single arming mechanism), keeps the
// next fire per trigger in memory, and timer-sleeps until the nearest one. Missed
// occurrences while the app was down are skipped rather than replayed — a
// recurring schedule simply resumes on its next tick.
type TriggerScheduler struct {
	store repository.Store
	// wake is a 1-buffered coalescing signal: many Notify() calls while the loop
	// is busy collapse into a single re-arm pass.
	wake chan struct{}
	// armed maps a schedule trigger id to its computed next fire. Kept across
	// passes so a trigger fires when its time is reached, not "next after now" on
	// every wake. Only the loop goroutine touches it.
	armed map[string]time.Time
}

// theTriggerScheduler is the process-wide instance controllers Notify when a
// schedule trigger is saved or deleted.
var theTriggerScheduler *TriggerScheduler

// StartTriggerScheduler creates the recurring scheduler and starts its loop.
// Call it after the inflow backend is initialized — firing needs a resource
// candid to Exec on, same as the process scheduler.
func StartTriggerScheduler(ctx context.Context, store repository.Store) *TriggerScheduler {
	s := &TriggerScheduler{store: store, wake: make(chan struct{}, 1), armed: map[string]time.Time{}}
	theTriggerScheduler = s
	go s.run(ctx)
	fmt.Println("trigger scheduler: started (recurring webhooks/schedules)")
	return s
}

// NotifyTriggerScheduler tells the recurring scheduler its set of schedule
// triggers changed (a save/delete) so it re-arms. No-op if none is running (e.g.
// the inflow runtime is disabled).
func NotifyTriggerScheduler() {
	if theTriggerScheduler != nil {
		select {
		case theTriggerScheduler.wake <- struct{}{}:
		default:
		}
	}
}

func (s *TriggerScheduler) run(ctx context.Context) {
	for {
		wait := s.dispatchAndArm(ctx)
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

// dispatchAndArm fires every schedule whose next occurrence has been reached,
// recomputes the next fire for each enabled schedule, forgets triggers that are
// no longer enabled, and returns how long to sleep until the nearest fire
// (bounded by the fallback).
func (s *TriggerScheduler) dispatchAndArm(ctx context.Context) time.Duration {
	triggers, err := s.store.Triggers().ListEnabledSchedules(ctx)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Printf("trigger scheduler: list schedules: %v\n", err)
		}
		return triggerSchedulerFallback
	}
	now := time.Now()
	wait := triggerSchedulerFallback
	seen := make(map[string]struct{}, len(triggers))

	for i := range triggers {
		t := &triggers[i]
		seen[t.ID] = struct{}{}

		spec := t.CronEffective
		if spec == "" {
			// A row written before normalization — derive the spec on the fly.
			if derived, derr := EffectiveCron(t); derr == nil {
				spec = derived
			}
		}

		next, ok := s.armed[t.ID]
		if !ok {
			n, nerr := NextFire(spec, now)
			if nerr != nil {
				fmt.Printf("trigger scheduler: bad spec for %s (%q): %v\n", t.ID, spec, nerr)
				continue
			}
			next = n
			s.armed[t.ID] = next
		}

		if !next.After(now) {
			// Due — fire once, then re-arm from now (skip any occurrences missed
			// while the app was down instead of storming through them).
			s.fire(ctx, t)
			n, nerr := NextFire(spec, now)
			if nerr != nil {
				delete(s.armed, t.ID)
				continue
			}
			next = n
			s.armed[t.ID] = next
		}

		if d := time.Until(next); d < wait {
			wait = d
		}
	}

	for id := range s.armed {
		if _, ok := seen[id]; !ok {
			delete(s.armed, id)
		}
	}
	if wait < 0 {
		wait = 0
	}
	return wait
}

// fire starts one due schedule trigger's flow and stamps its last-fire time. A
// launch failure is logged and surfaced as a notification; the trigger stays
// armed for its next occurrence.
func (s *TriggerScheduler) fire(ctx context.Context, t *models.Trigger) {
	if _, err := LaunchTrigger(ctx, s.store, t, "schedule: "+t.Title, nil); err != nil {
		fmt.Printf("trigger scheduler: launch %s (flow=%s): %v\n", t.ID, t.FlowID, err)
		wslog.Notify(wslog.LevelError, "Scheduled run failed",
			fmt.Sprintf("Trigger %s could not start flow %s: %v", t.Title, t.FlowID, err))
		return
	}
	_ = s.store.Triggers().MarkFired(ctx, t.ID, nowMillis())
	fmt.Printf("trigger scheduler: fired %s (flow=%s node=%s)\n", t.ID, t.FlowID, t.StartNodeID)
	wslog.Notify(wslog.LevelSuccess, "Scheduled run started",
		fmt.Sprintf("Trigger %s started flow %s", t.Title, t.FlowID))
}
