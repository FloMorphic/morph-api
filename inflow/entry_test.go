package inflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	// The sqlite backend registers the driver these tests open a store on.
	_ "github.com/FloMorphic/morph-api/repository/sqlite"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// The entry form is a contract between whoever launches a flow and the flow
// itself: the values are checked against the flow's own schema, and what lands
// in the context is only ever the one location the entry names.

func entryStore(t *testing.T) repository.Store {
	t.Helper()
	store, err := repository.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func schemaEntry() *models.EntryForm {
	return &models.EntryForm{
		Form: models.FormParameters{Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"orderId":   map[string]any{"type": "string"},
				"threshold": map[string]any{"type": "number", "default": float64(10)},
				"notify":    map[string]any{"type": "object", "properties": map[string]any{"email": map[string]any{"type": "string", "default": "ops@example.com"}}},
			},
			"required": []any{"orderId"},
		}},
	}
}

func TestPrepareEntryValuesFillsDefaultsAndValidates(t *testing.T) {
	entry := schemaEntry()

	vals, err := PrepareEntryValues(entry, map[string]any{"orderId": "A-1"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// A field left out comes back as the schema's own default — which is what
	// makes an unattended launch of a form-bearing flow safe.
	if vals["threshold"] != float64(10) {
		t.Errorf("threshold = %#v, want the schema default", vals["threshold"])
	}
	nested, _ := vals["notify"].(map[string]any)
	if nested["email"] != "ops@example.com" {
		t.Errorf("nested default missing: %#v", vals["notify"])
	}

	// A value the flow's own schema rejects stops the launch, and the message
	// names the field so the submitter can fix it.
	if _, err := PrepareEntryValues(entry, map[string]any{"orderId": float64(7)}); err == nil {
		t.Fatal("a wrongly typed value was accepted")
	} else if !strings.Contains(err.Error(), "orderId") {
		t.Errorf("error does not name the field: %v", err)
	}

	// A required field with no default is genuinely required.
	if _, err := PrepareEntryValues(entry, nil); err == nil {
		t.Fatal("a missing required field was accepted")
	}
}

func TestPrepareEntryValuesTakesPlainValuesAsWritten(t *testing.T) {
	// The easy way to declare an entry: the document IS the values to seed.
	// There is no contract to check it against, and the author's own values are
	// the defaults.
	entry := &models.EntryForm{Form: models.FormParameters{Schema: map[string]any{"stage": "review", "threshold": float64(3)}}}
	if entry.IsSchema() {
		t.Fatal("a plain values document read as a JSON Schema")
	}
	vals, err := PrepareEntryValues(entry, map[string]any{"threshold": float64(9)})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if vals["stage"] != "review" || vals["threshold"] != float64(9) {
		t.Errorf("values = %#v", vals)
	}
}

func TestSeedEntryWritesOnlyItsTarget(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	rec := &models.ContextRecord{Title: "run", Context: `{"order":{"id":"A-1"},"_sched":{"traverse":["n1"]}}`}
	if err := store.Contexts().Upsert(ctx, rec); err != nil {
		t.Fatal(err)
	}

	entry := schemaEntry()
	entry.Target = "$.input.form"
	if err := SeedEntry(ctx, store, entry, rec.ID, map[string]any{"orderId": "B-2"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	again, err := store.Contexts().GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal([]byte(again.Context), &doc); err != nil {
		t.Fatalf("stored context is not JSON: %v", err)
	}
	if v, ok := models.ValueAtContextPath(doc, "$.input.form.orderId"); !ok || v != "B-2" {
		t.Errorf("seed did not land at the target: %s", again.Context)
	}
	if _, ok := doc["_sched"]; !ok {
		t.Error("the seed dropped the engine's traversal header")
	}
	if order, _ := doc["order"].(map[string]any); order["id"] != "A-1" {
		t.Errorf("the seed disturbed the rest of the document: %s", again.Context)
	}
	// It is the flow's own data, attributed to the entry.
	if again.UpdatedBy.By != models.ByFlow || again.UpdatedBy.Address != MetaEntryKey {
		t.Errorf("updatedBy = %+v", again.UpdatedBy)
	}
}

func TestSeedEntryRefusesANonObjectContext(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	rec := &models.ContextRecord{Title: "odd", Context: `[1,2,3]`}
	if err := store.Contexts().Upsert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := SeedEntry(ctx, store, schemaEntry(), rec.ID, map[string]any{"orderId": "A-1"}); err == nil {
		t.Fatal("seeding into a non-object context was allowed")
	}
	again, _ := store.Contexts().GetByID(ctx, rec.ID)
	if strings.TrimSpace(again.Context) != `[1,2,3]` {
		t.Errorf("the refused seed changed the document: %s", again.Context)
	}
}

func TestResolveEntryViewPrefill(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	held := &models.ContextRecord{Title: "last run", Context: `{"_entry":{"orderId":"KEPT"}}`}
	if err := store.Contexts().Upsert(ctx, held); err != nil {
		t.Fatal(err)
	}

	flow := models.FlowRecord{Title: "with entry", Preferences: &models.FlowPreferences{Entry: schemaEntry()}}

	// Default mode shows what the flow would run with if nobody typed anything.
	view := ResolveEntryView(ctx, store, flow, held.ID)
	if view.Source != "default" || view.Values["threshold"] != float64(10) || view.Values["orderId"] != nil {
		t.Errorf("default view = %+v", view)
	}
	if view.Target != models.EntryTargetDefault {
		t.Errorf("target = %q", view.Target)
	}

	// Live mode starts the fields at what the context already holds, with the
	// schema's defaults still filling what it says nothing about.
	flow.Preferences.Entry.Prefill = models.PrefillLive
	view = ResolveEntryView(ctx, store, flow, held.ID)
	if view.Source != "context" || view.Values["orderId"] != "KEPT" || view.Values["threshold"] != float64(10) {
		t.Errorf("live view = %+v", view)
	}

	// A flow that declares nothing is not an error — it is a flow to launch as
	// it always was.
	plain := ResolveEntryView(ctx, store, models.FlowRecord{Title: "plain"}, held.ID)
	if plain.Entry != nil || plain.Source != "none" {
		t.Errorf("plain view = %+v", plain)
	}
}

// startFlow is a saved flow with just a start node — enough for StartWorkflow to
// resolve an entry node — carrying the given entry form.
func startFlow(t *testing.T, store repository.Store, entry *models.EntryForm) models.FlowRecord {
	t.Helper()
	rec := models.FlowRecord{
		Title:       "entry flow",
		Preferences: &models.FlowPreferences{Entry: entry},
		ViewFlow: compiler.VueFlow{Nodes: []compiler.VueFlowNode{
			{ID: "start", Type: NODE_START, Data: map[string]any{"title": "Start"}},
		}},
	}
	if err := store.Workflows().Upsert(context.Background(), &rec); err != nil {
		t.Fatalf("save flow: %v", err)
	}
	return rec
}

// The gate runs before anything is recorded: a launch that does not satisfy the
// flow's input contract is refused outright, not kept as a failed run.
func TestStartWorkflowRefusesAnUnsatisfiedRequiredEntry(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	entry := schemaEntry()
	entry.Required = true
	flow := startFlow(t, store, entry)
	cRec := &models.ContextRecord{Title: "empty", Context: "{}"}
	if err := store.Contexts().Upsert(ctx, cRec); err != nil {
		t.Fatal(err)
	}

	rec, err := StartWorkflow(ctx, store, StartParams{FlowID: flow.ID, ContextID: cRec.ID})
	if err == nil {
		t.Fatal("a flow with a required entry launched with nothing")
	}
	if rec != nil {
		t.Errorf("a refused launch recorded a row: %+v", rec)
	}
	if _, total, _ := store.Processes().List(ctx, repository.ListParams{Limit: 10}); total != 0 {
		t.Errorf("%d process rows recorded for a refused launch", total)
	}

	// A value the schema rejects is refused the same way.
	if _, err := StartWorkflow(ctx, store, StartParams{FlowID: flow.ID, ContextID: cRec.ID, Entry: map[string]any{"orderId": float64(7)}}); err == nil {
		t.Error("a wrongly typed entry value launched")
	}
}

// The seed lands before the run is dispatched, so the engine's very first node
// reads the input. (There is no engine in a test, so the dispatch itself fails —
// what matters here is that the context was seeded on the way.)
func TestStartWorkflowSeedsBeforeDispatch(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	flow := startFlow(t, store, schemaEntry())
	cRec := &models.ContextRecord{Title: "run", Context: `{"kept":true}`}
	if err := store.Contexts().Upsert(ctx, cRec); err != nil {
		t.Fatal(err)
	}

	_, _ = StartWorkflow(ctx, store, StartParams{
		FlowID:    flow.ID,
		ContextID: cRec.ID,
		Entry:     map[string]any{"orderId": "A-1"},
	})

	again, err := store.Contexts().GetByID(ctx, cRec.ID)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal([]byte(again.Context), &doc); err != nil {
		t.Fatalf("stored context: %v", err)
	}
	seeded, _ := doc["_entry"].(map[string]any)
	if seeded["orderId"] != "A-1" || seeded["threshold"] != float64(10) {
		t.Errorf("_entry = %#v (want the submitted value plus the schema default)", doc["_entry"])
	}
	if doc["kept"] != true {
		t.Error("the seed disturbed the rest of the document")
	}
}

// A scheduled launch is seeded by the scheduler at fire time, not when it was
// booked — the context should hold the run's input as the run starts. The values
// wait on the row for that moment.
func TestScheduledEntryIsSeededAtFireTime(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	flow := startFlow(t, store, schemaEntry())
	cRec := &models.ContextRecord{Title: "later", Context: `{"kept":true}`}
	if err := store.Contexts().Upsert(ctx, cRec); err != nil {
		t.Fatal(err)
	}

	// There is no engine in a test, so building the request fails and the row is
	// marked failed — after the two things under test have already happened:
	// the values were kept on the row, and the context was NOT seeded.
	rec, _ := StartWorkflow(ctx, store, StartParams{
		FlowID:      flow.ID,
		ContextID:   cRec.ID,
		Entry:       map[string]any{"orderId": "A-1"},
		ScheduledAt: nowMillis() + 3_600_000,
	})
	if rec == nil {
		t.Fatal("no row was recorded for a scheduled launch")
	}
	held, _ := store.Contexts().GetByID(ctx, cRec.ID)
	if strings.Contains(held.Context, "_entry") {
		t.Errorf("a scheduled launch seeded the context hours early: %s", held.Context)
	}
	if _, ok := rec.Meta[MetaEntryKey].(map[string]any); !ok {
		t.Fatalf("the values were not kept on the row: %#v", rec.Meta)
	}

	// What the scheduler does when the row comes due.
	if err := seedScheduledEntry(ctx, store, rec); err != nil {
		t.Fatalf("fire-time seed: %v", err)
	}
	again, _ := store.Contexts().GetByID(ctx, cRec.ID)
	doc := map[string]any{}
	_ = json.Unmarshal([]byte(again.Context), &doc)
	if seeded, _ := doc["_entry"].(map[string]any); seeded["orderId"] != "A-1" {
		t.Errorf("_entry = %#v", doc["_entry"])
	}
	if doc["kept"] != true {
		t.Error("the fire-time seed disturbed the rest of the document")
	}
}

// An unattended launch (a trigger, a schedule) supplies nothing. Its defaults
// must not take over values someone already put at the target.
func TestStartWorkflowDoesNotOverwriteHeldValuesWithDefaults(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	flow := startFlow(t, store, schemaEntry())
	cRec := &models.ContextRecord{Title: "already filled", Context: `{"_entry":{"orderId":"MINE","threshold":99}}`}
	if err := store.Contexts().Upsert(ctx, cRec); err != nil {
		t.Fatal(err)
	}

	_, _ = StartWorkflow(ctx, store, StartParams{FlowID: flow.ID, ContextID: cRec.ID})

	again, _ := store.Contexts().GetByID(ctx, cRec.ID)
	doc := map[string]any{}
	_ = json.Unmarshal([]byte(again.Context), &doc)
	seeded, _ := doc["_entry"].(map[string]any)
	if seeded["orderId"] != "MINE" || seeded["threshold"] != float64(99) {
		t.Errorf("_entry = %#v (an empty launch overwrote real input with defaults)", doc["_entry"])
	}
}

// An unattended launch — a trigger, a schedule, an agent that supplies nothing
// — must get past the entry when the author did not mark it required, even for
// a schema that declares required fields: `required: false` says this flow may
// run without input, and the graph guards the field it needs.
func TestStartWorkflowLetsAnOptionalEntryThrough(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	flow := startFlow(t, store, schemaEntry()) // schema requires orderId; entry is not required
	cRec := &models.ContextRecord{Title: "empty", Context: "{}"}
	if err := store.Contexts().Upsert(ctx, cRec); err != nil {
		t.Fatal(err)
	}

	// A row recorded means it got past the entry (the dispatch then fails — no
	// engine in a test).
	rec, _ := StartWorkflow(ctx, store, StartParams{FlowID: flow.ID, ContextID: cRec.ID})
	if rec == nil {
		t.Fatal("an optional entry refused an empty launch")
	}
	// The defaults were still seeded, so the flow starts from what its author
	// declared rather than from nothing.
	again, _ := store.Contexts().GetByID(ctx, cRec.ID)
	doc := map[string]any{}
	_ = json.Unmarshal([]byte(again.Context), &doc)
	if seeded, _ := doc["_entry"].(map[string]any); seeded["threshold"] != float64(10) {
		t.Errorf("_entry = %#v, want the document's defaults", doc["_entry"])
	}
}

// A required entry is satisfied by input the context already holds — and a
// schema-required field missing from an empty submit does not refuse the run
// when the values are already there.
func TestStartWorkflowRequiredEntrySatisfiedByTheContext(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()
	entry := schemaEntry()
	entry.Required = true
	flow := startFlow(t, store, entry)
	cRec := &models.ContextRecord{Title: "already filled", Context: `{"_entry":{"orderId":"MINE"}}`}
	if err := store.Contexts().Upsert(ctx, cRec); err != nil {
		t.Fatal(err)
	}

	rec, err := StartWorkflow(ctx, store, StartParams{FlowID: flow.ID, ContextID: cRec.ID})
	if rec == nil {
		t.Fatalf("a required entry refused a launch its context already satisfies: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "requires its entry form") {
		t.Errorf("refused by the entry gate: %v", err)
	}
	again, _ := store.Contexts().GetByID(ctx, cRec.ID)
	doc := map[string]any{}
	_ = json.Unmarshal([]byte(again.Context), &doc)
	seeded, _ := doc["_entry"].(map[string]any)
	if seeded["orderId"] != "MINE" || len(seeded) != 1 {
		t.Errorf("_entry = %#v (the launch should have left it untouched)", doc["_entry"])
	}
}
