package workflowControllers

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// A flow's `plugins` list is what tells a reader away from the canvas which
// plugin processes a flow needs — and which of them are the builtins that ship
// with FloMorphic — so the two surfaces it has to reach are the save response
// and the flow list, the one place a UI draws a flow without its graph.
//
// The graph below is the case the field exists for: a builtin plugin node and
// an imported plugin's action node wired into the flow, plus a plugin node
// parked on the canvas with nothing connected to it (a save warns about that
// one; it is not a plugin the flow needs). The payload also sends a `plugins`
// list of its own, which the save must ignore — the list is derived from the
// graph, never asserted by a client.
const pluginsFlowBody = `{
  "title": "plugin flow",
  "plugins": [{"kind": "extension", "pluginId": "not-in-this-graph"}],
  "view_flow": {
    "nodes": [
      {"id": "start", "type": "startNode", "data": {"title": "start"}},
      {"id": "ask", "type": "llm", "data": {"title": "ask", "pluginId": "aaaa-bbbb-cccc-llm0"}},
      {"id": "task", "type": "plugin", "data": {"title": "task", "pluginId": "jira-local", "action": "jira.add_task"}},
      {"id": "parked", "type": "http", "data": {"title": "parked", "pluginId": "aaaa-bbbb-cccc-http"}}
    ],
    "edges": [
      {"id": "e1", "source": "start", "target": "ask"},
      {"id": "e2", "source": "ask", "target": "task"}
    ]
  }
}`

func TestSavedFlowListsItsPlugins(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()

	req := httptest.NewRequest("POST", "/flow", bytes.NewReader([]byte(pluginsFlowBody)))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("POST /flow = %d", res.StatusCode)
	}
	var saved struct {
		Data models.FlowRecord `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}

	want := []models.FlowPlugin{
		{Kind: models.KindBuiltin, Type: models.ExtLLMType, PluginID: "aaaa-bbbb-cccc-llm0", NodeCount: 1},
		{Kind: models.KindExtension, Type: models.ExtPluginBaseType, PluginID: "jira-local",
			Actions: []string{"jira.add_task"}, NodeCount: 1},
	}
	assertPlugins(t, "save response", saved.Data.Plugins, want)
	if saved.Data.Plugins[0].Kind != models.KindBuiltin || saved.Data.Plugins[1].Kind != models.KindExtension {
		t.Fatalf("the builtin is not tagged as one: %+v", saved.Data.Plugins)
	}

	// The list is where the field earns its place: no graph on the row, and the
	// plugins still there.
	listed := httptest.NewRequest("GET", "/flow", nil)
	res, err = app.Test(listed, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("list request: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("GET /flow = %d", res.StatusCode)
	}
	var page struct {
		Data struct {
			List []models.FlowSummary `json:"list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Data.List) != 1 {
		t.Fatalf("list returned %d rows, want 1", len(page.Data.List))
	}
	assertPlugins(t, "list row", page.Data.List[0].Plugins, want)
}

func assertPlugins(t *testing.T, where string, got, want []models.FlowPlugin) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d plugins, want %d: %+v", where, len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Kind != w.Kind || g.Type != w.Type || g.PluginID != w.PluginID || g.NodeCount != w.NodeCount {
			t.Fatalf("%s: plugin %d = %+v, want %+v", where, i, g, w)
		}
		if len(g.Actions) != len(w.Actions) {
			t.Fatalf("%s: plugin %d actions = %v, want %v", where, i, g.Actions, w.Actions)
		}
		for j := range w.Actions {
			if g.Actions[j] != w.Actions[j] {
				t.Fatalf("%s: plugin %d actions = %v, want %v", where, i, g.Actions, w.Actions)
			}
		}
	}
}
