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

// TestProcessRunOutcomeRoundTrip pins the two run-outcome columns the inflow
// wire writes at the end of a run (storeRunOutcome): the traversal snapshot a
// continuation seeds from, and the error ledger a caller reads a run's health
// from.
//
// Both are set on the model by the wire and handed to Update, so a column the
// adapter does not carry loses them silently — the row still saves, the API
// still answers, and the ledger is simply never there. That is exactly how
// `errors` went missing, so the test reads them back off a fresh List (the path
// the process page actually uses) rather than off the struct it just wrote.
func TestProcessRunOutcomeRoundTrip(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	procs := store.Processes()

	p := &models.Process{PID: "pid-1", FlowID: "f", ContextID: "c", Status: models.ProcessRunning}
	if err := procs.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A run that hit two things and finished anyway — the case the row would
	// otherwise report as a clean completion.
	p.Status = models.ProcessFinished
	p.Snapshot = map[string]any{"flowSig": "sig-1"}
	p.Errors = map[string]any{
		"pid":   "pid-1",
		"count": float64(2),
		"items": []any{
			map[string]any{"ts": float64(7), "kind": "node", "flow": "f", "node": "n_1", "src": "js", "code": float64(25), "msg": "boom"},
			map[string]any{"ts": float64(9), "kind": "system", "flow": "f", "node": "n_2", "src": "rt", "code": float64(19), "msg": "infra"},
		},
	}
	if err := procs.Update(ctx, p); err != nil {
		t.Fatalf("update: %v", err)
	}

	items, _, err := procs.List(ctx, repository.ListParams{PID: "pid-1", Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("list returned %d rows, want 1", len(items))
	}
	got := items[0]

	if got.Snapshot["flowSig"] != "sig-1" {
		t.Errorf("snapshot = %+v, want flowSig sig-1", got.Snapshot)
	}
	if got.Errors == nil {
		t.Fatal("errors ledger is nil after a round trip — the column is not being carried")
	}
	if got.Errors["count"] != float64(2) {
		t.Errorf("errors count = %v, want 2", got.Errors["count"])
	}
	entries, ok := got.Errors["items"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("errors items = %#v, want 2 entries", got.Errors["items"])
	}
	first, _ := entries[0].(map[string]any)
	if first["kind"] != "node" || first["msg"] != "boom" || first["src"] != "js" {
		t.Errorf("first entry = %+v, want the node error it was stamped with", first)
	}
	// The kinds are not interchangeable: only one of them is the flow author's.
	if second, _ := entries[1].(map[string]any); second["kind"] != "system" {
		t.Errorf("second entry kind = %v, want system", second["kind"])
	}

	// A run that recorded nothing must stay empty, not arrive as a ledger with
	// no entries — the UI's "did this run hit anything" test is the field.
	clean := &models.Process{PID: "pid-2", FlowID: "f", ContextID: "c", Status: models.ProcessFinished}
	if err := procs.Create(ctx, clean); err != nil {
		t.Fatalf("create clean: %v", err)
	}
	back, err := procs.GetByIndex(ctx, clean.IndexID)
	if err != nil {
		t.Fatalf("get clean: %v", err)
	}
	if len(back.Errors) != 0 {
		t.Errorf("clean run errors = %+v, want empty", back.Errors)
	}
}
