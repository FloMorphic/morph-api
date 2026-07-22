package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

// TestScheduledProcessQueries exercises the two queries the scheduler is built
// on: NextScheduled (the row the timer arms from) and ListDueScheduled (the
// batch dispatched when it fires). Ordering, the due cutoff, and the
// status='scheduled' filter are the contract.
func TestScheduledProcessQueries(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	procs := store.Processes()

	// Empty table: nothing armed, nothing due.
	if _, err := procs.NextScheduled(ctx); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("NextScheduled on empty = %v, want ErrNotFound", err)
	}

	now := int64(1_000_000)
	mk := func(status models.ProcessStatus, at int64) *models.Process {
		p := &models.Process{FlowID: "f", ContextID: "c", Status: status, ScheduledAt: at}
		if err := procs.Create(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		return p
	}
	overdue := mk(models.ProcessScheduled, now-500) // due
	dueNow := mk(models.ProcessScheduled, now)      // due (<= now)
	future := mk(models.ProcessScheduled, now+500)  // not yet
	mk(models.ProcessRunning, now-900)              // wrong status: never scheduled

	next, err := procs.NextScheduled(ctx)
	if err != nil {
		t.Fatalf("NextScheduled: %v", err)
	}
	if next.IndexID != overdue.IndexID {
		t.Fatalf("NextScheduled = %d, want earliest scheduled %d", next.IndexID, overdue.IndexID)
	}

	due, err := procs.ListDueScheduled(ctx, now)
	if err != nil {
		t.Fatalf("ListDueScheduled: %v", err)
	}
	if len(due) != 2 || due[0].IndexID != overdue.IndexID || due[1].IndexID != dueNow.IndexID {
		t.Fatalf("ListDueScheduled = %+v, want [%d %d] soonest-first", due, overdue.IndexID, dueNow.IndexID)
	}

	// Dispatch claims the row (scheduled → running); it must drop out of both.
	overdue.Status = models.ProcessRunning
	if err := procs.Update(ctx, overdue); err != nil {
		t.Fatalf("update: %v", err)
	}
	dueNow.Status = models.ProcessRunning
	if err := procs.Update(ctx, dueNow); err != nil {
		t.Fatalf("update: %v", err)
	}

	next, err = procs.NextScheduled(ctx)
	if err != nil {
		t.Fatalf("NextScheduled after claim: %v", err)
	}
	if next.IndexID != future.IndexID {
		t.Fatalf("NextScheduled after claim = %d, want %d", next.IndexID, future.IndexID)
	}
	if due, err = procs.ListDueScheduled(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("ListDueScheduled after claim = %v, %v; want empty", due, err)
	}
}
