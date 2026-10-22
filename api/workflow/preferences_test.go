package workflowControllers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// A flow's entry form is edited on its own route and resolved for a launch on
// another, so these cover the three things a launcher depends on: setting a
// form without touching the graph, reading it back resolved, and a graph save
// never silently dropping it.

func putJSON(t *testing.T, app *fiber.App, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func getJSON(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest("GET", path, nil), fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func entryPrefs() map[string]any {
	return map[string]any{
		"entry": map[string]any{
			"title":   "Order to review",
			"form":    map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"orderId": map[string]any{"type": "string", "default": "A-1"}}}},
			"prefill": "live",
		},
	}
}

func TestSetAndResolveFlowEntry(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()
	ctx := context.Background()

	flow := models.FlowRecord{Title: "needs input"}
	if err := store.Workflows().Upsert(ctx, &flow); err != nil {
		t.Fatal(err)
	}

	if code, _ := putJSON(t, app, "/flow/id/"+flow.ID+"/preferences", map[string]any{"preferences": entryPrefs()}); code != 200 {
		t.Fatalf("PUT preferences = %d", code)
	}

	// Read back: the declared form, and the target defaulted to the standard slot.
	code, body := getJSON(t, app, "/flow/id/"+flow.ID+"/entry")
	if code != 200 {
		t.Fatalf("GET entry = %d", code)
	}
	data, _ := body["data"].(map[string]any)
	if data["target"] != models.EntryTargetDefault {
		t.Errorf("target = %#v", data["target"])
	}
	values, _ := data["values"].(map[string]any)
	if values["orderId"] != "A-1" {
		t.Errorf("values = %#v (want the schema default)", values)
	}
	if data["source"] != "default" {
		t.Errorf("source = %#v", data["source"])
	}

	// Live prefill against a context that already holds values starts the
	// fields there instead.
	held := &models.ContextRecord{Title: "last run", Context: `{"_entry":{"orderId":"KEPT"}}`}
	if err := store.Contexts().Upsert(ctx, held); err != nil {
		t.Fatal(err)
	}
	_, body = getJSON(t, app, "/flow/id/"+flow.ID+"/entry?contextId="+held.ID)
	data, _ = body["data"].(map[string]any)
	values, _ = data["values"].(map[string]any)
	if values["orderId"] != "KEPT" || data["source"] != "context" {
		t.Errorf("live view = %#v", data)
	}
}

func TestEntrySurvivesAGraphSaveAndClearsOnDemand(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()
	ctx := context.Background()

	flow := models.FlowRecord{Title: "needs input"}
	if err := store.Workflows().Upsert(ctx, &flow); err != nil {
		t.Fatal(err)
	}
	if code, _ := putJSON(t, app, "/flow/id/"+flow.ID+"/preferences", map[string]any{"preferences": entryPrefs()}); code != 200 {
		t.Fatalf("PUT preferences = %d", code)
	}

	// A save that says nothing about preferences — a graph-only save from the
	// canvas, an MCP patch — must not strip the form.
	raw, _ := json.Marshal(map[string]any{"id": flow.ID, "title": "needs input", "view_flow": map[string]any{"nodes": []any{}, "edges": []any{}}})
	req := httptest.NewRequest("POST", "/flow", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if res, err := app.Test(req, fiber.TestConfig{Timeout: 0}); err != nil || res.StatusCode != 200 {
		t.Fatalf("POST /flow = %v %v", res, err)
	}
	again, err := store.Workflows().GetByID(ctx, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Preferences.EntryOrNil() == nil {
		t.Fatal("a graph-only save dropped the flow's entry form")
	}

	// An empty preferences object on the route is the clear.
	if code, _ := putJSON(t, app, "/flow/id/"+flow.ID+"/preferences", map[string]any{"preferences": map[string]any{}}); code != 200 {
		t.Fatalf("clear = %d", code)
	}
	again, _ = store.Workflows().GetByID(ctx, flow.ID)
	if again.Preferences.EntryOrNil() != nil {
		t.Errorf("preferences = %+v, want cleared", again.Preferences)
	}
}

func TestEntryTargetMustNameOneLocation(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()
	ctx := context.Background()
	flow := models.FlowRecord{Title: "needs input"}
	if err := store.Workflows().Upsert(ctx, &flow); err != nil {
		t.Fatal(err)
	}
	prefs := entryPrefs()
	prefs["entry"].(map[string]any)["target"] = "$.items[*]"
	// A target that selects many locations is refused at save time: it would
	// otherwise only surface as a failed run, long after the dialog closed.
	if code, _ := putJSON(t, app, "/flow/id/"+flow.ID+"/preferences", map[string]any{"preferences": prefs}); code != 400 {
		t.Errorf("PUT with a wildcard target = %d, want 400", code)
	}
}

func TestExportCarriesPreferences(t *testing.T) {
	app, store := importApp(t, false)
	defer store.Close()
	ctx := context.Background()

	flow := models.FlowRecord{Title: "needs input", Preferences: &models.FlowPreferences{Entry: &models.EntryForm{
		Form: models.FormParameters{Schema: map[string]any{"type": "object", "properties": map[string]any{"orderId": map[string]any{"type": "string"}}}},
	}}}
	if err := store.Workflows().Upsert(ctx, &flow); err != nil {
		t.Fatal(err)
	}
	_, body := getJSON(t, app, "/flow/id/"+flow.ID+"/export")
	data, _ := body["data"].(map[string]any)
	// The entry form is part of the design, so it travels with the portable
	// document: a flow installed elsewhere asks for its input there too.
	if _, ok := data["preferences"].(map[string]any); !ok {
		t.Errorf("export dropped the preferences: %#v", data)
	}
}
