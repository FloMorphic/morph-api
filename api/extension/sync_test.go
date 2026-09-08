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
		Outbound: []models.OutboundPort{
			{Title: "Approved", Tags: []string{"approved"}, Description: "passed review"},
			{Title: "Rejected", Tags: []string{"rejected"}},
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
	// A freshly built row carries no id; sync fills in the id of the row this
	// action already had, and leaves it empty only for an action that is new.
	if row.ID != "" {
		t.Errorf("id = %q, want empty so sync decides reuse-or-insert", row.ID)
	}
	// Declared outbound ports are carried through verbatim for the canvas to
	// render one port per entry.
	if len(row.Outbound) != 2 {
		t.Fatalf("outbound = %+v, want the action's 2 ports", row.Outbound)
	}
	if row.Outbound[0].Title != "Approved" || len(row.Outbound[0].Tags) != 1 || row.Outbound[0].Tags[0] != "approved" {
		t.Errorf("outbound[0] = %+v, want the Approved port with tag approved", row.Outbound[0])
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
	// No outbound declared → none carried; the node keeps its single default port.
	if len(row.Outbound) != 0 {
		t.Errorf("outbound = %+v, want none for an action that declares no ports", row.Outbound)
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

// The point of the index: a re-sync of a plugin that still exposes the same
// action must land on the row that action already has, so the id a saved
// workflow stores keeps resolving.
func TestActionIndexMatchesExistingRow(t *testing.T) {
	rows := []models.ExtensionRecord{
		{ID: "ext_add", Name: "Add task", Action: "add_task"},
		{ID: "ext_close", Name: "Close task", Action: "close_task"},
	}
	idx := newActionIndex(rows)

	// Same method, retitled by the plugin: still the same row.
	if got := idx.claim("add_task", "Create task"); got == nil || got.ID != "ext_add" {
		t.Errorf("claim(add_task) = %+v, want ext_add", got)
	}
	// Method renamed, same title: matched on the name instead.
	if got := idx.claim("archive_task", "Close task"); got == nil || got.ID != "ext_close" {
		t.Errorf("claim by name = %+v, want ext_close", got)
	}
	// Nothing left to match, so a genuinely new action is an insert.
	if got := idx.claim("assign_task", "Assign task"); got != nil {
		t.Errorf("claim for a new action = %+v, want nil", got)
	}
	if left := idx.unclaimed(); len(left) != 0 {
		t.Errorf("unclaimed = %+v, want none", left)
	}
}

// A row is claimed once. Two actions that both point at it must not both
// overwrite it — the second is a new node, and the first keeps the row.
func TestActionIndexClaimsEachRowOnce(t *testing.T) {
	idx := newActionIndex([]models.ExtensionRecord{{ID: "ext_add", Name: "Add task", Action: "add_task"}})

	if got := idx.claim("add_task", "Add task"); got == nil || got.ID != "ext_add" {
		t.Fatalf("first claim = %+v, want ext_add", got)
	}
	if got := idx.claim("add_task_v2", "Add task"); got != nil {
		t.Errorf("second claim on the same row = %+v, want nil", got)
	}
}

// A method the plugin dropped leaves its row unclaimed — that is the set sync
// deletes, and the only thing it deletes.
func TestActionIndexReportsDroppedRows(t *testing.T) {
	idx := newActionIndex([]models.ExtensionRecord{
		{ID: "ext_add", Name: "Add task", Action: "add_task"},
		{ID: "ext_gone", Name: "Old task", Action: "old_task"},
	})
	idx.claim("add_task", "Add task")

	left := idx.unclaimed()
	if len(left) != 1 || left[0].ID != "ext_gone" {
		t.Errorf("unclaimed = %+v, want just ext_gone", left)
	}
}
