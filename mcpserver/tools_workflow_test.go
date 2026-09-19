package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	_ "github.com/FloMorphic/morph-api/repository/sqlite"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// toolServer is an in-process MCP server over a fresh in-memory store with the
// workflow + designer tools, plus one imported plugin action so plugin stamping
// is exercised end to end.
func toolServer(t *testing.T) (*server.MCPServer, repository.Store) {
	t.Helper()
	store, err := repository.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	reg := models.ExtensionRecord{Kind: models.KindExtension, Type: "plugin", Name: "venapce", PluginID: "venapce-1234"}
	if err := store.Extensions().Upsert(ctx, &reg); err != nil {
		t.Fatal(err)
	}
	row := models.ExtensionRecord{Kind: models.KindExtension, Type: "plugin", Name: "Run osquery", PluginID: reg.PluginID, ParentID: reg.ID, Action: "osquery.query",
		Parameters: models.FormParameters{Schema: map[string]any{"type": "object"}}}
	if err := store.Extensions().Upsert(ctx, &row); err != nil {
		t.Fatal(err)
	}
	s := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
	registerWorkflowTools(s, store)
	registerDesignerTools(s, store)
	return s, store
}

// call invokes a tool the way a client would (a JSON-RPC tools/call) and
// returns the decoded JSON text of its first content block, or fails the test
// on a tool error unless wantErr.
func call(t *testing.T, s *server.MCPServer, tool string, args map[string]any, wantErr bool) map[string]any {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, params)
	resp := s.HandleMessage(context.Background(), json.RawMessage(msg))
	raw, _ := json.Marshal(resp)
	var env struct {
		Result mcp.CallToolResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s: decode response: %v\n%s", tool, err, raw)
	}
	if env.Error != nil {
		t.Fatalf("%s: protocol error: %s", tool, env.Error.Message)
	}
	text := ""
	if len(env.Result.Content) > 0 {
		if tc, ok := env.Result.Content[0].(mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if env.Result.IsError != wantErr {
		t.Fatalf("%s: isError=%v, want %v: %s", tool, env.Result.IsError, wantErr, text)
	}
	if wantErr {
		return map[string]any{"error": text}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: result is not JSON: %v\n%s", tool, err, text)
	}
	return out
}

func TestImportExportTools(t *testing.T) {
	s, store := toolServer(t)
	doc := map[string]any{
		"title":   "Probe",
		"plugins": []any{map[string]any{"name": "venapce", "actions": []any{"osquery.query", "db.stages.upsert"}}},
		"nodes": []any{
			map[string]any{"ref": "start", "kind": "startNode", "title": "Start"},
			map[string]any{"ref": "probe", "kind": "plugin", "title": "Probe", "key": "probe", "data": map[string]any{"action": "osquery.query", "body": map[string]any{"sql": "select 1"}}},
			map[string]any{"ref": "sink", "kind": "plugin", "title": "Sink", "data": map[string]any{"action": "db.stages.upsert"}},
		},
		"edges": []any{
			map[string]any{"from": "start", "to": "probe"},
			map[string]any{"from": "probe", "to": "sink"},
		},
	}

	// Dry run: reports, saves nothing.
	out := call(t, s, "flo_import_workflow", map[string]any{"workflow": doc, "dryRun": true}, false)
	if out["dryRun"] != true || out["ok"] != true {
		t.Fatalf("dry run: %v", out)
	}
	missing := out["missingActions"].([]any)
	if len(missing) != 1 || missing[0].(map[string]any)["action"] != "db.stages.upsert" {
		t.Fatalf("missingActions = %v", missing)
	}
	if _, total, _ := store.Workflows().List(context.Background(), repository.ListParams{Limit: 5}); total != 0 {
		t.Fatal("dry run saved a flow")
	}

	// Real import lands the flow, title overridden.
	out = call(t, s, "flo_import_workflow", map[string]any{"workflow": doc, "title": "Renamed"}, false)
	flow := out["flow"].(map[string]any)
	id, _ := flow["id"].(string)
	if id == "" || flow["title"] != "Renamed" {
		t.Fatalf("import: %v", flow)
	}
	rec, err := store.Workflows().GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	stamped := 0
	for _, n := range rec.ViewFlow.Nodes {
		if d := n.Data.(map[string]any); n.Type == "plugin" && d["pluginId"] == "venapce-1234" {
			stamped++
		}
	}
	if stamped != 1 {
		t.Fatalf("expected the resolvable plugin node stamped, got %d", stamped)
	}

	// Export: the compact document, no local identity, the manifest names both
	// plugins' actions.
	exp := call(t, s, "flo_export_workflow", map[string]any{"id": id}, false)
	raw, _ := json.Marshal(exp)
	if strings.Contains(string(raw), "venapce-1234") || strings.Contains(string(raw), "extensionId") {
		t.Fatalf("export leaks install identity: %s", raw)
	}
	if exp["title"] != "Renamed" || len(exp["nodes"].([]any)) != 3 || len(exp["edges"].([]any)) != 2 {
		t.Fatalf("export shape: %s", raw)
	}
	full, _ := json.Marshal(rec)
	if len(raw) > len(full)/2 {
		t.Errorf("export (%d bytes) should be well under half the record (%d)", len(raw), len(full))
	}

	// Re-import over the same id (a re-install) keeps one flow.
	out = call(t, s, "flo_import_workflow", map[string]any{"workflow": exp, "id": id}, false)
	if out["flow"].(map[string]any)["id"] != id {
		t.Fatalf("re-import changed id: %v", out["flow"])
	}
	if _, total, _ := store.Workflows().List(context.Background(), repository.ListParams{Limit: 5}); total != 1 {
		t.Fatalf("expected 1 flow after re-import, got %d", total)
	}

	// Rejections come back as tool errors.
	call(t, s, "flo_import_workflow", map[string]any{"workflow": map[string]any{"title": "x"}}, true)
	call(t, s, "flo_import_workflow", map[string]any{"workflow": map[string]any{"nodes": []any{map[string]any{"ref": "a", "kind": "js", "title": "A"}}}}, true)
	call(t, s, "flo_export_workflow", map[string]any{"id": "flow_nope"}, true)
}

func TestPatchToolsStampPlugins(t *testing.T) {
	s, _ := toolServer(t)
	nodes := []any{
		map[string]any{"ref": "start", "kind": "startNode", "title": "Start"},
		map[string]any{"ref": "probe", "kind": "plugin", "title": "Probe", "data": map[string]any{"action": "osquery.query"}},
	}
	edges := []any{map[string]any{"from": "start", "to": "probe"}}

	plan := call(t, s, "flo_plan_patch", map[string]any{"nodes": nodes, "edges": edges}, false)
	if _, ok := plan["problems"]; !ok {
		t.Fatalf("plan has no problems list: %v", plan)
	}
	if len(plan["missingActions"].([]any)) != 0 {
		t.Fatalf("plan should resolve the local action: %v", plan["missingActions"])
	}
	// A fragment without a startNode is reported, not raised.
	frag := call(t, s, "flo_plan_patch", map[string]any{"nodes": nodes[1:]}, false)
	if frag["ok"] != false || frag["error"] == nil {
		t.Fatalf("fragment plan: %v", frag)
	}

	applied := call(t, s, "flo_apply_patch", map[string]any{"title": "Patched", "nodes": nodes, "edges": edges}, false)
	flow := applied["flow"].(map[string]any)
	for _, n := range flow["view_flow"].(map[string]any)["nodes"].([]any) {
		node := n.(map[string]any)
		if node["type"] == "plugin" && node["data"].(map[string]any)["pluginId"] != "venapce-1234" {
			t.Fatalf("apply_patch did not stamp the plugin node: %v", node["data"])
		}
	}
	call(t, s, "flo_apply_patch", map[string]any{"nodes": nodes}, true) // title required
}
