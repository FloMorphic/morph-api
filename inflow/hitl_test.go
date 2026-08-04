package inflow

import (
	"testing"

	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
)

// buildHitl compiles a single HITL node and returns the `op` payload the
// extrinsic will ship to svc.hitl.add.
func buildHitl(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	node := inflowModels.Node{ID: "h", Title: "Ask"}
	if err := buildHitlNode(&node, compiler.VueFlowNode{ID: "h", Type: NODE_HITL, Data: data}, data); err != nil {
		t.Fatalf("buildHitlNode: %v", err)
	}
	if node.Type != inflowModels.ExtrinsicNodeType {
		t.Fatalf("node type = %v, want extrinsic", node.Type)
	}
	if node.Extrinsic == nil {
		t.Fatal("node has no extrinsic rule")
	}
	return node.Extrinsic.Data
}

// The whole session config has to survive compilation: the handler cannot
// recover any of it from the flow at run time.
func TestBuildHitlNodeShipsSessionConfig(t *testing.T) {
	refs := []any{map[string]any{"id": "ref-1", "name": "thread", "path": "$.messages"}}
	questions := []any{map[string]any{"id": "q1", "text": "Approve?"}}
	payload := buildHitl(t, map[string]any{
		"title":     "Ask a human",
		"mode":      "continue",
		"channel":   "telegram",
		"prompt":    "Review {{$.messages}} and answer.",
		"refs":      refs,
		"questions": questions,
	})

	if payload["nodeId"] != "h" {
		t.Fatalf("nodeId = %v, want h", payload["nodeId"])
	}
	if payload["mode"] != "continue" {
		t.Fatalf("mode = %v, want continue", payload["mode"])
	}
	if payload["channel"] != "telegram" {
		t.Fatalf("channel = %v, want telegram", payload["channel"])
	}
	// The prompt travels as written — resolving it here is impossible and
	// rewriting it would lose the variables the session needs.
	if payload["prompt"] != "Review {{$.messages}} and answer." {
		t.Fatalf("prompt = %v", payload["prompt"])
	}
	if payload["refs"] == nil || payload["questions"] == nil {
		t.Fatalf("refs/questions dropped: %+v", payload)
	}
}

// A node authored before mode/channel existed must compile to the behaviour it
// has always had rather than to an empty string the handler would have to guess
// at: park, answered in the app.
func TestBuildHitlNodeDefaultsLegacyNode(t *testing.T) {
	payload := buildHitl(t, map[string]any{"title": "Ask a human"})
	if payload["mode"] != "park" {
		t.Fatalf("mode = %v, want park", payload["mode"])
	}
	if payload["channel"] != "direct" {
		t.Fatalf("channel = %v, want direct", payload["channel"])
	}
	// Nothing was authored, so nothing beyond the defaults is shipped.
	if _, ok := payload["prompt"]; ok {
		t.Fatalf("unauthored prompt was shipped: %+v", payload)
	}
}

// An unknown mode/channel (hand-edited JSON, an imported graph) falls back to
// the safe pair instead of reaching the handler as garbage.
func TestBuildHitlNodeNarrowsUnknownValues(t *testing.T) {
	payload := buildHitl(t, map[string]any{"mode": "whenever", "channel": "carrier-pigeon"})
	if payload["mode"] != "park" {
		t.Fatalf("mode = %v, want park", payload["mode"])
	}
	if payload["channel"] != "direct" {
		t.Fatalf("channel = %v, want direct", payload["channel"])
	}
}
