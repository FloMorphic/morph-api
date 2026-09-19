package flowfile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/designer"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	_ "github.com/FloMorphic/morph-api/repository/sqlite"
)

// A store with one imported plugin ("venapce") exposing two actions, one of
// them with outbound ports, so stamping and port resolution are both exercised.
func openStore(t *testing.T) repository.Store {
	t.Helper()
	store, err := repository.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	reg := models.ExtensionRecord{Kind: models.KindExtension, Type: "plugin", Name: "venapce", PluginID: "venapce-1234",
		Install: models.InstallSpec{Repo: "https://example.test/venapce.git", Ref: "main"}}
	if err := store.Extensions().Upsert(ctx, &reg); err != nil {
		t.Fatal(err)
	}
	rows := []models.ExtensionRecord{
		{Name: "Run osquery", Action: "osquery.query"},
		{Name: "Upsert stage", Action: "db.stages.upsert", Outbound: []models.OutboundPort{
			{Title: "Stored", Tags: []string{"stored"}}, {Title: "Failed", Tags: []string{"failed"}},
		}},
	}
	for _, r := range rows {
		r.Kind, r.Type, r.PluginID, r.ParentID = models.KindExtension, "plugin", reg.PluginID, reg.ID
		r.Parameters = models.FormParameters{Schema: map[string]any{"type": "object"}}
		if err := store.Extensions().Upsert(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

const sample = `{
  "flomorphic": {"kind": "workflow", "version": 1},
  "title": "Audit",
  "plugins": [{"name": "venapce", "actions": ["osquery.query", "db.stages.upsert"], "repo": "https://example.test/venapce.git"}],
  "nodes": [
    {"ref": "start", "kind": "startNode", "title": "Start"},
    {"ref": "probe", "kind": "plugin", "title": "Probe", "key": "probe", "data": {"action": "osquery.query", "body": {"sql": "select 1"}}},
    {"ref": "sink", "kind": "plugin", "title": "Store", "data": {"action": "db.stages.upsert", "pluginId": "foreign-9999", "extensionId": "ext_foreign"}},
    {"ref": "summarise", "kind": "llm", "title": "Summarise", "data": {"prompt": "Summarise {{$.probe}}"}},
    {"ref": "done", "kind": "promissall", "title": "Done"}
  ],
  "edges": [
    {"from": "start", "to": "probe"},
    {"from": "probe", "to": "sink"},
    {"from": "sink", "to": "summarise", "port": "Stored"},
    {"from": "sink", "to": "done", "port": "failed"},
    {"from": "summarise", "to": "done"}
  ]
}`

func TestImportStampsAndRoutesPluginPorts(t *testing.T) {
	store := openStore(t)
	doc, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Import(context.Background(), store, Input{Doc: doc})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.MissingActions) != 0 {
		t.Fatalf("unexpected missing actions: %+v", res.MissingActions)
	}
	if res.Flow.ID == "" || res.Flow.Title != "Audit" {
		t.Fatalf("flow not saved as titled: %+v", res.Flow.Title)
	}
	for _, p := range res.Problems {
		if p.Level == "error" {
			t.Errorf("plan error: %+v", p)
		}
	}
	// The caller's document is untouched.
	if _, ok := doc.Nodes[1].Data["extensionId"]; ok {
		t.Error("Import mutated the input document")
	}

	var sinkID string
	for _, n := range res.Flow.ViewFlow.Nodes {
		d := n.Data.(map[string]any)
		switch n.Type {
		case "plugin":
			if d["pluginId"] != "venapce-1234" || d["extensionId"] == "" || d["form"] == nil {
				t.Errorf("plugin node %q not stamped locally: %v", d["title"], d)
			}
			if d["title"] == "Store" {
				sinkID = n.ID
				if len(d["outbound"].([]any)) != 2 {
					t.Errorf("outbound not stamped: %v", d["outbound"])
				}
			}
		case "llm":
			// A builtin plugin-backed kind gets its builtin row's identity.
			if d["extensionId"] == "" {
				t.Errorf("llm node not stamped with its builtin row: %v", d)
			}
		}
	}
	// The plugin's outbound ports resolved the edges' route tags.
	tags := map[string]string{}
	for _, e := range res.Flow.ViewFlow.Edges {
		if e.Source == sinkID {
			tags[strings.Join(e.Data.Tags, ",")] = e.SourceHandle
		}
	}
	if tags["stored"] != "Stored" || tags["failed"] != "Failed" {
		t.Fatalf("plugin edges not routed by outbound: %v", tags)
	}
	if res.StartNodeID == "" && res.CompileError == "" {
		t.Error("neither a compiled graph nor a compile error reported")
	}
}

func TestImportFlagsMissingAndDryRun(t *testing.T) {
	store, _ := repository.Open("sqlite", ":memory:")
	doc, _ := Parse([]byte(sample))
	res, err := Import(context.Background(), store, Input{Doc: doc, Title: "Renamed", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Flow.Title != "Renamed" || !res.DryRun {
		t.Fatalf("title override / dryRun lost: %+v", res.Flow.Title)
	}
	if len(res.MissingActions) != 2 {
		t.Fatalf("expected 2 missing actions, got %+v", res.MissingActions)
	}
	if m := res.MissingActions[0]; m.Plugin != "venapce" || m.Repo == "" || len(m.Nodes) != 1 || m.Nodes[0] != "probe" {
		t.Errorf("missing action should name the plugin, repo and caller refs: %+v", m)
	}
	for _, n := range res.Flow.ViewFlow.Nodes {
		if n.Type == "plugin" {
			d := n.Data.(map[string]any)
			if _, ok := d["missingPlugin"]; !ok {
				t.Errorf("plugin node not marked missing: %v", d)
			}
			if _, ok := d["pluginId"]; ok {
				t.Errorf("foreign pluginId must be cleared: %v", d)
			}
		}
	}
	if _, total, _ := store.Workflows().List(context.Background(), repository.ListParams{Limit: 10}); total != 0 {
		t.Fatalf("dry run saved %d flows", total)
	}
}

func TestImportRejects(t *testing.T) {
	store, _ := repository.Open("sqlite", ":memory:")
	if _, err := Parse([]byte(`{"title": "x"}`)); err == nil {
		t.Error("a document without nodes must not parse")
	}
	doc := Document{Nodes: []designer.PatchNode{{Ref: "a", Kind: "js", Title: "A"}}}
	_, err := Import(context.Background(), store, Input{Doc: doc})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a patch without a startNode must be ErrInvalid, got %v", err)
	}
}

func TestExportRoundTrip(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	doc, _ := Parse([]byte(sample))
	res, err := Import(ctx, store, Input{Doc: doc})
	if err != nil {
		t.Fatal(err)
	}
	out := Export(ctx, store, res.Flow)
	if out.Flomorphic == nil || out.Flomorphic.Kind != "workflow" || out.Flomorphic.Version != Version {
		t.Fatalf("header: %+v", out.Flomorphic)
	}
	if out.Title != "Audit" {
		t.Errorf("title: %q", out.Title)
	}
	// Manifest: one plugin, both actions, named and located from the registration row.
	if len(out.Plugins) != 1 || out.Plugins[0].Name != "venapce" || len(out.Plugins[0].Actions) != 2 || out.Plugins[0].Repo == "" {
		t.Fatalf("manifest: %+v", out.Plugins)
	}
	// Nothing install-local leaks, and plugin edges keep their port names.
	raw, _ := json.Marshal(out)
	for _, leak := range []string{"venapce-1234", "extensionId", `"form"`, `"outbound"`, "\"n-", "\"e-"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("export leaks %q:\n%s", leak, raw)
		}
	}
	ports := map[string]bool{}
	for _, e := range out.Edges {
		if e.Port != "" {
			ports[e.From+"#"+e.Port] = true
		}
	}
	if !ports["store#stored"] || !ports["store#failed"] {
		t.Errorf("plugin ports not exported: %v", ports)
	}
	// And the export is smaller than the record it came from.
	full, _ := json.Marshal(res.Flow)
	if len(raw) >= len(full)/2 {
		t.Errorf("export %d bytes vs record %d — expected well under half", len(raw), len(full))
	}
	// Re-import lands the same graph shape.
	again, err := Import(ctx, store, Input{Doc: out, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Flow.ViewFlow.Nodes) != 5 || len(again.Flow.ViewFlow.Edges) != 5 || len(again.MissingActions) != 0 {
		t.Fatalf("re-import: %d nodes %d edges missing=%v", len(again.Flow.ViewFlow.Nodes), len(again.Flow.ViewFlow.Edges), again.MissingActions)
	}
}
