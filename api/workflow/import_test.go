package workflowControllers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	_ "github.com/FloMorphic/morph-api/repository/sqlite"
	"github.com/gofiber/fiber/v3"
)

// The cookbook's fleet audit export, as a package installer would send it: a
// real multi-node graph (three plugin sweeps, promissall, js, rule, four
// scoped plugin probes, a db.stages.upsert sink) whose plugin nodes carry only
// their action. With the venapce plugin's action rows synced the nodes must
// come out stamped; without them they must be flagged, never dropped.
const cookbookFlow = "../../../flow-cookbook/linux-fleet-http-audit/linux-fleet-http-nginx-served-hostnames-audit.flow.json"

func importApp(t *testing.T, seedVenapce bool) (*fiber.App, repository.Store) {
	t.Helper()
	store, err := repository.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if seedVenapce {
		ctx := context.Background()
		reg := models.ExtensionRecord{Kind: models.KindExtension, Type: "plugin", Name: "venapce", PluginID: "venapce-1234"}
		if err := store.Extensions().Upsert(ctx, &reg); err != nil {
			t.Fatal(err)
		}
		for _, a := range []string{"osquery.query", "osquery.queryByTags", "db.stages.upsert"} {
			row := models.ExtensionRecord{Kind: models.KindExtension, Type: "plugin", Name: "venapce · " + a, PluginID: "venapce-1234", Action: a, ParentID: reg.ID,
				Parameters: models.FormParameters{Schema: map[string]any{"type": "object"}}}
			if err := store.Extensions().Upsert(ctx, &row); err != nil {
				t.Fatal(err)
			}
		}
	}
	app := fiber.New()
	Register(app, store)
	return app, store
}

func postImport(t *testing.T, app *fiber.App, body map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/flow/import", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, out)
	}
	return out
}

func loadCookbook(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(cookbookFlow)
	if err != nil {
		t.Skipf("cookbook export not checked out next to this repo: %v", err)
	}
	return b
}

func TestImportStampsLocalPlugins(t *testing.T) {
	app, store := importApp(t, true)
	out := postImport(t, app, map[string]any{"workflow": loadCookbook(t)})
	data := out["data"].(map[string]any)
	if data["ok"] != true {
		t.Fatalf("import not ok: %v", data["error"])
	}
	if n := len(data["missingActions"].([]any)); n != 0 {
		t.Fatalf("expected no missing actions, got %v", data["missingActions"])
	}
	flow := data["flow"].(map[string]any)
	id, _ := flow["id"].(string)
	if id == "" {
		t.Fatal("saved flow has no id")
	}
	if flow["title"] != "Linux fleet — HTTP / nginx + served hostnames audit" {
		t.Fatalf("title not taken from the file: %v", flow["title"])
	}
	rec, err := store.Workflows().GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("saved flow not readable: %v", err)
	}
	plugins := 0
	for _, n := range rec.ViewFlow.Nodes {
		if n.Type != "plugin" {
			continue
		}
		plugins++
		d := n.Data.(map[string]any)
		if d["pluginId"] != "venapce-1234" || d["extensionId"] == "" {
			t.Errorf("plugin node %s not stamped: %v", n.ID, d)
		}
		if _, ok := d["form"]; !ok {
			t.Errorf("plugin node %s has no form", n.ID)
		}
	}
	if plugins != 8 {
		t.Fatalf("expected 8 plugin nodes, got %d", plugins)
	}
	if len(rec.ViewFlow.Edges) != 19 {
		t.Fatalf("expected 19 edges, got %d", len(rec.ViewFlow.Edges))
	}
}

func TestImportFlagsMissingPlugins(t *testing.T) {
	app, store := importApp(t, false)
	out := postImport(t, app, map[string]any{"workflow": loadCookbook(t), "title": "Renamed", "dryRun": true})
	data := out["data"].(map[string]any)
	if data["ok"] != true {
		t.Fatalf("import not ok: %v", data["error"])
	}
	missing := data["missingActions"].([]any)
	if len(missing) != 3 {
		t.Fatalf("expected 3 missing actions, got %v", missing)
	}
	first := missing[0].(map[string]any)
	if first["plugin"] != "venapce" {
		t.Fatalf("plugin name should come from the file's manifest, got %v", first)
	}
	flow := data["flow"].(map[string]any)
	if flow["title"] != "Renamed" {
		t.Fatalf("title override ignored: %v", flow["title"])
	}
	// Dry run: nothing saved.
	if _, total, _ := store.Workflows().List(context.Background(), repository.ListParams{Limit: 10}); total != 0 {
		t.Fatalf("dry run saved %d flows", total)
	}
	for _, n := range flow["view_flow"].(map[string]any)["nodes"].([]any) {
		node := n.(map[string]any)
		if node["type"] != "plugin" {
			continue
		}
		if _, ok := node["data"].(map[string]any)["missingPlugin"]; !ok {
			t.Errorf("plugin node %v not marked missing", node["id"])
		}
	}
}

func TestImportRejectsNonWorkflow(t *testing.T) {
	app, _ := importApp(t, false)
	raw, _ := json.Marshal(map[string]any{"workflow": map[string]any{"title": "x"}})
	req := httptest.NewRequest("POST", "/flow/import", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, _ := app.Test(req, fiber.TestConfig{Timeout: 0})
	if res.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", res.StatusCode)
	}
}

func TestExportRoute(t *testing.T) {
	app, _ := importApp(t, true)
	out := postImport(t, app, map[string]any{"workflow": loadCookbook(t)})
	id := out["data"].(map[string]any)["flow"].(map[string]any)["id"].(string)

	res, err := app.Test(httptest.NewRequest("GET", "/flow/id/"+id+"/export", nil), fiber.TestConfig{Timeout: 0})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("export: %v status %d", err, res.StatusCode)
	}
	var env struct {
		Data struct {
			Title   string           `json:"title"`
			Plugins []map[string]any `json:"plugins"`
			Nodes   []map[string]any `json:"nodes"`
			Edges   []map[string]any `json:"edges"`
		} `json:"data"`
	}
	body, _ := json.Marshal(mustDecode(t, res.Body))
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	d := env.Data
	if d.Title == "" || len(d.Nodes) != 15 || len(d.Edges) != 19 {
		t.Fatalf("export shape: title=%q nodes=%d edges=%d", d.Title, len(d.Nodes), len(d.Edges))
	}
	if len(d.Plugins) != 1 || d.Plugins[0]["name"] != "venapce" {
		t.Fatalf("manifest: %v", d.Plugins)
	}
	if bytes.Contains(body, []byte("venapce-1234")) || bytes.Contains(body, []byte("extensionId")) {
		t.Fatal("export leaks install-local identity")
	}
	// The exported document is a valid import again.
	postImport(t, app, map[string]any{"workflow": json.RawMessage(mustRaw(t, d)), "dryRun": true})
}

func mustDecode(t *testing.T, r interface{ Read([]byte) (int, error) }) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustRaw(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
