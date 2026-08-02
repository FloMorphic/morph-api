package extensionControllers

import (
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// A synced row has to carry everything a palette node needs without going back
// to the plugin: the plugin's identity, the method to call, and the action's own
// form. Getting any of those wrong produces a node that looks right and does
// nothing.
func TestActionRow(t *testing.T) {
	parent := &models.ExtensionRecord{
		ID: "ext_1", Name: "Jira", PluginID: "jira-274b",
		Icon: models.Icon{Class: "flomorphic", Name: "plugin"},
	}
	act := models.PluginAction{
		Method: "add_task", Title: "Add task", Description: "Create an issue",
		Icon: models.PluginIcon{Ref: "lucide", Icon: "plus"},
		Form: models.FormBuilder{
			Jsonschema: `{"type":"object","properties":{"summary":{"type":"string"}}}`,
			Jsonui:     `{"summary":{"ui:widget":"textarea"}}`,
		},
	}

	row := actionRow(parent, act, act.Method)

	if row.PluginID != parent.PluginID {
		t.Errorf("pluginId = %q, want the parent's %q", row.PluginID, parent.PluginID)
	}
	if row.Action != "add_task" {
		t.Errorf("action = %q, want add_task", row.Action)
	}
	if row.ParentID != "ext_1" {
		t.Errorf("parentId = %q, want ext_1", row.ParentID)
	}
	if row.Type != models.ExtPluginBaseType || row.Kind != models.KindExtension {
		t.Errorf("row is %s/%s, want extension/plugin", row.Kind, row.Type)
	}
	if row.Name != "Add task" {
		t.Errorf("name = %q, want the action title", row.Name)
	}
	if row.Icon.Name != "plus" || row.Icon.Class != "lucide" {
		t.Errorf("icon = %+v, want the action's own", row.Icon)
	}
	// The SDK ships the form as JSON-in-a-string; the row stores it parsed.
	props, ok := row.Parameters.Schema["properties"].(map[string]any)
	if !ok || props["summary"] == nil {
		t.Errorf("schema not parsed from the form string: %+v", row.Parameters.Schema)
	}
	if row.Parameters.UI["summary"] == nil {
		t.Errorf("ui schema not parsed: %+v", row.Parameters.UI)
	}
	// A row never carries an id: it is a fresh insert on every sync.
	if row.ID != "" {
		t.Errorf("id = %q, want empty so the repo issues one", row.ID)
	}
}

// An action with no title, no icon and no form is still a usable node — it just
// falls back to the method name and the plugin's own icon.
func TestActionRowFallbacks(t *testing.T) {
	parent := &models.ExtensionRecord{
		ID: "ext_1", Name: "Jira", PluginID: "jira-274b",
		Icon: models.Icon{Class: "flomorphic", Name: "plugin"},
	}
	row := actionRow(parent, models.PluginAction{Method: "ping"}, "ping")

	if row.Name != "ping" {
		t.Errorf("name = %q, want the method name", row.Name)
	}
	if row.Icon.Name != "plugin" {
		t.Errorf("icon = %+v, want the plugin's", row.Icon)
	}
	if row.Parameters.Schema == nil || len(row.Parameters.Schema) != 0 {
		t.Errorf("schema = %+v, want an empty object", row.Parameters.Schema)
	}
}

// A plugin sending a malformed form must not fail the sync — the action is still
// callable, it just has no fields to render.
func TestFormParametersToleratesGarbage(t *testing.T) {
	got := formParameters(models.FormBuilder{Jsonschema: "{not json", Jsonui: ""})
	if got.Schema == nil || got.UI == nil {
		t.Fatalf("nil maps for an unparseable form: %+v", got)
	}
	if len(got.Schema) != 0 || len(got.UI) != 0 {
		t.Errorf("got %+v, want empty objects", got)
	}
}
