package flowfile

import (
	"context"
	"testing"

	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	_ "github.com/FloMorphic/morph-api/repository/sqlite"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// storeWithProfile is a real store holding one provider profile, so these tests
// exercise the path an import actually takes — the profile resolves, and what the
// node keeps of it is the thing under test.
func storeWithProfile(t *testing.T, settings map[string]any) (repository.Store, string) {
	t.Helper()
	store, err := repository.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	prof := &models.NodeSetting{NodeUniqID: "hitl", NodeType: "hitl", Title: "gemini-HITL", Settings: settings}
	if err := store.NodeSettings().Upsert(context.Background(), prof); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return store, prof.ID
}

// A HITL node binds its chat provider BY ID: the backend reads the profile from
// the store at conversation time, so copying the values onto the node buys nothing
// and writes a provider access token into the saved graph. The editor has always
// excluded it; import must too, or the same flow is safe when drawn and leaky when
// imported.
//
// The profile here DOES resolve, which is the case that used to leak: the token was
// copied onto the node exactly because the lookup succeeded.
func TestResolveSettingsProfilesNeverCopiesOntoHitl(t *testing.T) {
	store, id := storeWithProfile(t, map[string]any{"provider": "gemini", "access_token": "secret-token"})
	graph := &compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{
			{
				ID:   "h",
				Type: inflow.NODE_HITL,
				Data: map[string]any{
					"settingsId": id,
					// A hand-written or third-party document could carry values here;
					// import must strip them rather than pass them through.
					"settings": map[string]any{"access_token": "smuggled-token"},
				},
			},
		},
	}
	resolveSettingsProfiles(context.Background(), store, graph)

	data := graph.Nodes[0].Data.(map[string]any)
	if _, ok := data["settings"]; ok {
		t.Fatalf("a HITL node kept settings on its data: %+v", data)
	}
	// The binding itself must survive — it is how the chat service finds the profile.
	if data["settingsId"] != id {
		t.Fatalf("settingsId = %v, want it preserved", data["settingsId"])
	}
	// The label is the one thing it may take, so the drawer can name the binding.
	if data["settingsName"] != "gemini-HITL" {
		t.Fatalf("settingsName = %v, want the profile title", data["settingsName"])
	}
}

// The same node with an unresolvable profile must also come back clean — that is the
// other branch, and the one where a stray `settings` key would be easiest to miss.
func TestResolveSettingsProfilesHitlWithMissingProfile(t *testing.T) {
	store, _ := storeWithProfile(t, map[string]any{})
	graph := &compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{
			{ID: "h", Type: inflow.NODE_HITL, Data: map[string]any{
				"settingsId": "nset_gone",
				"settings":   map[string]any{"access_token": "smuggled-token"},
			}},
		},
	}
	resolveSettingsProfiles(context.Background(), store, graph)

	data := graph.Nodes[0].Data.(map[string]any)
	if _, ok := data["settings"]; ok {
		t.Fatalf("a HITL node with no profile kept settings: %+v", data)
	}
	if data["settingsName"] != "" {
		t.Fatalf("settingsName = %v, want cleared", data["settingsName"])
	}
}

// A plugin-lowered node is the opposite case: its settings are shipped to the
// plugin as part of the compiled body, so they DO have to be denormalized. With no
// profile to resolve it gets an empty map rather than nothing, which is what tells
// the compiler the binding was seen and came back empty.
func TestResolveSettingsProfilesStillDenormalizesPluginNodes(t *testing.T) {
	store, _ := storeWithProfile(t, map[string]any{})
	graph := &compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{
			{ID: "p", Type: inflow.NODE_PLUGIN, Data: map[string]any{"settingsId": "nset_gone"}},
		},
	}
	resolveSettingsProfiles(context.Background(), store, graph)

	data := graph.Nodes[0].Data.(map[string]any)
	settings, ok := data["settings"].(map[string]any)
	if !ok {
		t.Fatalf("a plugin node lost its settings map: %+v", data)
	}
	if len(settings) != 0 {
		t.Fatalf("settings = %+v, want empty for an unresolvable profile", settings)
	}
}

// A node that binds no profile is left completely alone — neither key invented.
func TestResolveSettingsProfilesIgnoresUnboundNodes(t *testing.T) {
	store, _ := storeWithProfile(t, map[string]any{})
	graph := &compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{{ID: "j", Type: inflow.NODE_JS, Data: map[string]any{}}},
	}
	resolveSettingsProfiles(context.Background(), store, graph)
	if data := graph.Nodes[0].Data.(map[string]any); len(data) != 0 {
		t.Fatalf("an unbound node was given settings keys: %+v", data)
	}
}
