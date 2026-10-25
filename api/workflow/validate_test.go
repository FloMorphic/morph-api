package workflowControllers

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// Saving a flow reports what compiling it found, and GET …/compile reports it
// either way — on the payload when the flow is sound, as the refusal's details
// when it is not. That is what lets the editor show the mistake while the author
// is still on the canvas, instead of it surfacing as a run that hangs.

// deadlockGraph is the shape the engine's depends guard exists for: a loop back
// into a fan-in join, which makes the join wait for a node that only runs after
// it.
func deadlockGraph() map[string]any {
	node := func(id, kind, title string) map[string]any {
		return map[string]any{"id": id, "type": kind, "data": map[string]any{"title": title, "scope": "$"}}
	}
	js := func(id, title string) map[string]any {
		return map[string]any{"id": id, "type": "js", "data": map[string]any{
			"title": title, "key": id, "scope": "$", "lang": "js", "logic_rule": "return {}",
		}}
	}
	edge := func(id, from, to string) map[string]any {
		return map[string]any{"id": id, "source": from, "target": to}
	}
	return map[string]any{
		"nodes": []map[string]any{
			node("s", "startNode", "Start"), js("a", "A"),
			node("j", "promissall", "Join"), js("after", "After join"),
		},
		"edges": []map[string]any{
			edge("e1", "s", "a"), edge("e2", "a", "j"),
			edge("e3", "j", "after"), edge("e_back", "after", "j"),
		},
	}
}

func postFlow(t *testing.T, app *fiber.App, body map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/flow", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("POST /flow = %d", res.StatusCode)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Data
}

func TestUpsertReportsValidationWithoutRefusing(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()

	saved := postFlow(t, app, map[string]any{"title": "Deadlocked", "view_flow": deadlockGraph()})

	// The save went through — a half-wired canvas has to be savable — and the
	// record is still the response payload, so an existing client is unaffected.
	id, _ := saved["id"].(string)
	if id == "" {
		t.Fatalf("saved = %v, want the flow record with its id", saved)
	}
	validation, ok := saved["validation"].(map[string]any)
	if !ok {
		t.Fatalf("saved = %v, want a validation payload", saved)
	}
	if validation["ok"] != false {
		t.Fatalf("validation = %v, want ok=false", validation)
	}
	issues, _ := validation["issues"].([]any)
	if len(issues) == 0 {
		t.Fatalf("validation = %v, want the deadlock issue", validation)
	}
	first, _ := issues[0].(map[string]any)
	if first["code"] != "depend_deadlock" || first["nodeId"] != "j" || first["edgeId"] != "e_back" {
		t.Fatalf("issue = %v, want the join and the back edge named", first)
	}
	if first["message"] == "" || first["suggestion"] == "" {
		t.Fatalf("issue = %v, want a message and a suggested fix to show", first)
	}

	// The same flow still COMPILES: a guard finding is advice about one
	// traversal, never a refusal, and the route's default pass does not even
	// draw it. Nothing here keeps the flow from being lowered or run.
	plain := getCompile(t, app, id, "")
	if plain["startNodeId"] != "s" {
		t.Fatalf("compile = %v, want the lowered graph", plain)
	}
	if _, reported := plain["validation"]; reported {
		t.Fatalf("compile = %v, want no validation unless it is asked for", plain)
	}

	// Asked for, it comes back with the issues — and still 200.
	checked := getCompile(t, app, id, "?validate=true")
	v, _ := checked["validation"].(map[string]any)
	if v == nil || v["ok"] != false {
		t.Fatalf("validated compile = %v, want the issues reported", checked)
	}
	if checked["nodes"] == nil {
		t.Fatalf("validated compile = %v, want the lowered nodes too", checked)
	}
}

// getCompile reads GET /flow/id/:id/compile, failing the test on any non-200:
// the guard must never change this route's status code.
func getCompile(t *testing.T, app *fiber.App, id, query string) map[string]any {
	t.Helper()
	res, err := app.Test(httptest.NewRequest("GET", "/flow/id/"+id+"/compile"+query, nil), fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("GET compile%s = %d, want 200", query, res.StatusCode)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Data
}

func TestCompileRouteReportsWarningsOnASoundFlow(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()

	graph := map[string]any{
		"nodes": []map[string]any{
			{"id": "s", "type": "startNode", "data": map[string]any{"title": "Start", "scope": "$"}},
			{"id": "a", "type": "js", "data": map[string]any{
				"title": "A", "key": "a", "scope": "$", "lang": "js", "logic_rule": "return {}"}},
			// Saved but wired to nothing: it compiles, it never runs.
			{"id": "orphan", "type": "js", "data": map[string]any{
				"title": "Orphan", "key": "o", "scope": "$", "lang": "js", "logic_rule": "return {}"}},
		},
		"edges": []map[string]any{{"id": "e1", "source": "s", "target": "a"}},
	}
	saved := postFlow(t, app, map[string]any{"title": "Sound", "view_flow": graph})
	id, _ := saved["id"].(string)

	res, err := app.Test(httptest.NewRequest("GET", "/flow/id/"+id+"/compile?validate=true", nil), fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("GET compile = %d, want 200 — a warning never refuses", res.StatusCode)
	}
	var out struct {
		Data struct {
			StartNodeID string         `json:"startNodeId"`
			Nodes       map[string]any `json:"nodes"`
			Validation  struct {
				OK           bool `json:"ok"`
				WarningCount int  `json:"warningCount"`
				Issues       []struct {
					Code     string `json:"code"`
					NodeID   string `json:"nodeId"`
					Severity string `json:"severity"`
				} `json:"issues"`
			} `json:"validation"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.StartNodeID != "s" || out.Data.Nodes["a"] == nil {
		t.Fatalf("compile = %+v, want the lowered graph", out.Data)
	}
	v := out.Data.Validation
	if !v.OK || v.WarningCount != 1 || len(v.Issues) != 1 {
		t.Fatalf("validation = %+v, want ok with one warning", v)
	}
	if v.Issues[0].NodeID != "orphan" || v.Issues[0].Severity != "warning" ||
		v.Issues[0].Code != "node_unreachable" {
		t.Fatalf("issue = %+v, want the orphan warned about", v.Issues[0])
	}
}
