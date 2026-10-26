package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// TestWorkflowTags covers the three things the tag filter rests on: tags are
// normalized on the way in, a save that says nothing about them does not strip
// them, and the list filter is a match-ANY membership test that pages
// correctly (the count must agree with the rows).
func TestWorkflowTags(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	flows := store.Workflows()

	mk := func(title string, tags []string) *models.FlowRecord {
		rec := &models.FlowRecord{Title: title, Tags: tags}
		if err := flows.Upsert(ctx, rec); err != nil {
			t.Fatalf("upsert %q: %v", title, err)
		}
		return rec
	}

	// Normalization: case, padding, an inner space, a comma (which would break
	// the delimited filter), and a duplicate.
	messy := mk("messy", []string{" GDPR ", "ISO 27001", "a,b", "gdpr", ""})
	if got, want := strings.Join(messy.Tags, "|"), "gdpr|iso-27001|ab"; got != want {
		t.Fatalf("normalized tags = %q, want %q", got, want)
	}

	fraud := mk("fraud sweep", []string{"fraud-surveillance"})
	credit := mk("credit watch", []string{"credit-risk", "fraud-surveillance"})
	mk("untagged helper", nil)

	// A re-read carries the tags back.
	again, err := flows.GetByID(ctx, credit.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Join(again.Tags, "|") != "credit-risk|fraud-surveillance" {
		t.Fatalf("stored tags = %v", again.Tags)
	}

	// A save with nil tags (an MCP patch, an older client) keeps them; an
	// explicit empty list clears them.
	again.Tags = nil
	again.Title = "credit watch v2"
	if err := flows.Upsert(ctx, again); err != nil {
		t.Fatalf("upsert without tags: %v", err)
	}
	if strings.Join(again.Tags, "|") != "credit-risk|fraud-surveillance" {
		t.Fatalf("tags lost on a tag-less save: %v", again.Tags)
	}

	// Rows created in the same millisecond tie on `updated_at` and fall back to
	// a random id, so compare sets of titles, not order.
	list := func(tags ...string) []string {
		items, total, err := flows.List(ctx, repository.ListParams{Limit: 50, Tags: tags})
		if err != nil {
			t.Fatalf("list %v: %v", tags, err)
		}
		if int(total) != len(items) {
			t.Fatalf("list %v: total %d but %d rows", tags, total, len(items))
		}
		titles := make([]string, 0, len(items))
		for _, it := range items {
			titles = append(titles, it.Title)
		}
		sort.Strings(titles)
		return titles
	}

	// No filter is the whole list — clearing the filter needs no second path.
	if got := list(); len(got) != 4 {
		t.Fatalf("unfiltered list = %v, want 4 rows", got)
	}
	if got := strings.Join(list("fraud-surveillance"), "|"); got != "credit watch v2|fraud sweep" {
		t.Fatalf("one-tag filter = %q", got)
	}
	// Match ANY, not all.
	if got := len(list("credit-risk", "fraud-surveillance")); got != 2 {
		t.Fatalf("two-tag filter returned %d rows, want 2", got)
	}
	// Unknown tags match nothing, and the filter is exact per element: a tag
	// that is a prefix of a stored one must not match it.
	if got := list("nope"); len(got) != 0 {
		t.Fatalf("unknown tag matched %v", got)
	}
	if got := list("fraud"); len(got) != 0 {
		t.Fatalf("prefix tag matched %v", got)
	}
	// A filter the caller typed loosely still matches.
	if got := len(list(" FRAUD-Surveillance ")); got != 2 {
		t.Fatalf("loose filter returned %d rows, want 2", got)
	}

	// Clearing: an explicit empty list drops them, and the row leaves the filter.
	fraud.Tags = []string{}
	if err := flows.Upsert(ctx, fraud); err != nil {
		t.Fatalf("clear tags: %v", err)
	}
	if got := strings.Join(list("fraud-surveillance"), "|"); got != "credit watch v2" {
		t.Fatalf("after clearing, filter = %q", got)
	}
}

// A flow's preferences (its entry form) follow the same "a save that says
// nothing keeps what is stored" contract the tags do — the one thing that keeps
// a graph-only save, from any client, from dropping the form a flow needs.
func TestWorkflowPreferences(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	flows := store.Workflows()

	rec := &models.FlowRecord{Title: "needs input", Preferences: &models.FlowPreferences{Entry: &models.EntryForm{
		Title:    "Order",
		Form:     models.FormParameters{Schema: map[string]any{"type": "object", "properties": map[string]any{"orderId": map[string]any{"type": "string"}}}},
		Target:   "$.input.form",
		Prefill:  models.PrefillLive,
		Required: true,
	}}}
	if err := flows.Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	again, err := flows.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry := again.Preferences.EntryOrNil()
	if entry == nil {
		t.Fatal("preferences did not round-trip")
	}
	if entry.TargetPath() != "$.input.form" || entry.PrefillOrDefault() != models.PrefillLive || !entry.Required {
		t.Errorf("entry = %+v", entry)
	}

	// A save that says nothing about them keeps them…
	if err := flows.Upsert(ctx, &models.FlowRecord{ID: rec.ID, Title: "needs input"}); err != nil {
		t.Fatalf("upsert without preferences: %v", err)
	}
	again, _ = flows.GetByID(ctx, rec.ID)
	if again.Preferences.EntryOrNil() == nil {
		t.Fatal("a preference-less save dropped the entry form")
	}

	// …and a non-nil value with no entry clears them.
	if err := flows.Upsert(ctx, &models.FlowRecord{ID: rec.ID, Title: "needs input", Preferences: &models.FlowPreferences{}}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	again, _ = flows.GetByID(ctx, rec.ID)
	if again.Preferences != nil {
		t.Errorf("preferences = %+v, want cleared", again.Preferences)
	}
}

// TestWorkflowListSummary covers the one thing a flow list must NOT do: carry
// the graph. A row comes back as a summary — no view_flow on the wire and none
// read from the column — with the node count the query derived from the stored
// graph standing in for it, including for the rows the count has to survive: a
// flow with no graph, and the empty text an un-migrated row holds.
func TestWorkflowListSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flows.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	flows := store.Workflows()

	graphed := &models.FlowRecord{Title: "graphed", ViewFlow: compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{{ID: "n1"}, {ID: "n2"}, {ID: "n3"}},
		Edges: []compiler.Edges{{ID: "e1", Source: "n1", Target: "n2"}},
	}}
	if err := flows.Upsert(ctx, graphed); err != nil {
		t.Fatalf("upsert graphed: %v", err)
	}
	empty := &models.FlowRecord{Title: "empty"}
	if err := flows.Upsert(ctx, empty); err != nil {
		t.Fatalf("upsert empty: %v", err)
	}
	// An un-migrated row: view_flow as the blank text the column could hold
	// before it had a default, which json_valid is what guards the count from.
	legacy := &models.FlowRecord{Title: "legacy"}
	if err := flows.Upsert(ctx, legacy); err != nil {
		t.Fatalf("upsert legacy: %v", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE workflows SET view_flow = '' WHERE id = ?`, legacy.ID); err != nil {
		t.Fatalf("blank view_flow: %v", err)
	}

	items, total, err := flows.List(ctx, repository.ListParams{Limit: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("list returned %d rows (total %d), want 3", len(items), total)
	}
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Title] = it.NodeCount
	}
	if counts["graphed"] != 3 {
		t.Fatalf("node count = %d, want 3", counts["graphed"])
	}
	if counts["empty"] != 0 || counts["legacy"] != 0 {
		t.Fatalf("graph-less rows counted %d / %d nodes, want 0", counts["empty"], counts["legacy"])
	}

	// The graph is absent from the wire shape, not merely empty on it: a list
	// response must never carry a view_flow key (see models.FlowSummary).
	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal summaries: %v", err)
	}
	if strings.Contains(string(payload), "view_flow") {
		t.Fatalf("list payload carries the graph: %s", payload)
	}

	// …and the whole flow is still one GetByID away.
	full, err := flows.GetByID(ctx, graphed.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(full.ViewFlow.Nodes) != 3 || len(full.ViewFlow.Edges) != 1 {
		t.Fatalf("get lost the graph: %d nodes, %d edges", len(full.ViewFlow.Nodes), len(full.ViewFlow.Edges))
	}

	// ListRecords is the same page, read whole — the listing the MCP tool uses
	// so a detailed answer costs one query rather than a read per row. Same
	// rows, same total, same order; graphs included.
	recs, recTotal, err := flows.ListRecords(ctx, repository.ListParams{Limit: 50})
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if recTotal != total || len(recs) != len(items) {
		t.Fatalf("records page = %d rows (total %d), summaries = %d (total %d)", len(recs), recTotal, len(items), total)
	}
	for i, rec := range recs {
		if rec.ID != items[i].ID {
			t.Fatalf("row %d: records %s, summaries %s — the pages disagree", i, rec.ID, items[i].ID)
		}
		if len(rec.ViewFlow.Nodes) != items[i].NodeCount {
			t.Fatalf("row %d (%s): %d nodes in the record, nodeCount %d", i, rec.ID, len(rec.ViewFlow.Nodes), items[i].NodeCount)
		}
	}
}

// TestWorkflowPlugins covers the derived plugin list end to end: a save
// computes it from the graph (ignoring whatever the caller sent), a list row
// carries it without reading a graph, and a flow of plugin-less nodes carries
// none.
func TestWorkflowPlugins(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	flows := store.Workflows()

	rec := &models.FlowRecord{
		Title: "with plugins",
		// A list the caller invented: the save replaces it with the graph's own
		// plugins, so a client can never put a plugin on a flow that does not
		// use one.
		Plugins: []models.FlowPlugin{{Kind: models.KindExtension, PluginID: "not-in-this-graph"}},
		ViewFlow: compiler.VueFlow{
			Nodes: []compiler.VueFlowNode{
				{ID: "start", Type: "startNode"},
				{ID: "ask", Type: "llm", Data: map[string]any{"pluginId": "aaaa-bbbb-cccc-llm0"}},
				{ID: "task", Type: "plugin", Data: map[string]any{"pluginId": "jira-local", "action": "jira.add_task"}},
				// Wired to nothing: not a plugin this flow needs.
				{ID: "parked", Type: "http", Data: map[string]any{"pluginId": "aaaa-bbbb-cccc-http"}},
			},
			Edges: []compiler.Edges{
				{ID: "e1", Source: "start", Target: "ask"},
				{ID: "e2", Source: "ask", Target: "task"},
			},
		},
	}
	if err := flows.Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	want := []models.FlowPlugin{
		{Kind: models.KindBuiltin, Type: models.ExtLLMType, PluginID: "aaaa-bbbb-cccc-llm0", NodeCount: 1},
		{Kind: models.KindExtension, Type: models.ExtPluginBaseType, PluginID: "jira-local",
			Actions: []string{"jira.add_task"}, NodeCount: 1},
	}
	if !reflect.DeepEqual(rec.Plugins, want) {
		t.Fatalf("saved plugins = %+v, want %+v", rec.Plugins, want)
	}

	got, err := flows.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got.Plugins, want) {
		t.Fatalf("stored plugins = %+v, want %+v", got.Plugins, want)
	}

	// A plugin-less flow stores no list, and a list row carries both answers —
	// read from the column, with no graph in sight.
	plain := &models.FlowRecord{Title: "no plugins", ViewFlow: compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{{ID: "start", Type: "startNode"}, {ID: "code", Type: "js"}},
		Edges: []compiler.Edges{{ID: "e1", Source: "start", Target: "code"}},
	}}
	if err := flows.Upsert(ctx, plain); err != nil {
		t.Fatalf("upsert plain: %v", err)
	}
	items, _, err := flows.List(ctx, repository.ListParams{Limit: 50})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listed := map[string][]models.FlowPlugin{}
	for _, it := range items {
		listed[it.Title] = it.Plugins
	}
	if !reflect.DeepEqual(listed["with plugins"], want) {
		t.Fatalf("listed plugins = %+v, want %+v", listed["with plugins"], want)
	}
	if listed["no plugins"] != nil {
		t.Fatalf("plugin-less flow listed %+v, want none", listed["no plugins"])
	}

	// A save that only relabels still resends the record it read, so the list
	// has to survive the round trip rather than be rebuilt from nothing.
	got.Tags = []string{"ops"}
	if err := flows.Upsert(ctx, got); err != nil {
		t.Fatalf("relabel: %v", err)
	}
	again, err := flows.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get after relabel: %v", err)
	}
	if !reflect.DeepEqual(again.Plugins, want) {
		t.Fatalf("plugins after relabel = %+v, want %+v", again.Plugins, want)
	}
}

// TestWorkflowPluginsBackfill covers the one-time pass for the flows that were
// saved before the column existed: on the next boot they carry their derived
// list, rather than claiming to need no plugin until someone re-saves each one.
func TestWorkflowPluginsBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flows.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()

	rec := &models.FlowRecord{Title: "pre-column", ViewFlow: compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{
			{ID: "start", Type: "startNode"},
			{ID: "ask", Type: "llm", Data: map[string]any{"pluginId": "aaaa-bbbb-cccc-llm0"}},
		},
		Edges: []compiler.Edges{{ID: "e1", Source: "start", Target: "ask"}},
	}}
	plain := &models.FlowRecord{Title: "pre-column plain"}
	for _, f := range []*models.FlowRecord{rec, plain} {
		if err := store.Workflows().Upsert(ctx, f); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	// Back to what an un-migrated row looks like: the sentinel the ALTER leaves.
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflows SET plugins = ''`); err != nil {
		t.Fatalf("clear plugins: %v", err)
	}
	var updatedAt int64
	if err := db.QueryRowContext(ctx, `SELECT updated_at FROM workflows WHERE id = ?`, rec.ID).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	db.Close()
	store.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Workflows().GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := []models.FlowPlugin{{
		Kind: models.KindBuiltin, Type: models.ExtLLMType,
		PluginID: "aaaa-bbbb-cccc-llm0", NodeCount: 1,
	}}
	if !reflect.DeepEqual(got.Plugins, want) {
		t.Fatalf("backfilled plugins = %+v, want %+v", got.Plugins, want)
	}
	// Deriving a field is not a change to the flow: the list must not reorder.
	if got.UpdatedAt != updatedAt {
		t.Fatalf("backfill moved updated_at from %d to %d", updatedAt, got.UpdatedAt)
	}
	// A flow that needs no plugin is now marked derived, so the pass is done:
	// a third open must find nothing left to fill.
	reopened.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store again: %v", err)
	}
	defer again.Close()
	db, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	var pending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE plugins = ''`).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Fatalf("%d row(s) still underived after the backfill", pending)
	}
}
