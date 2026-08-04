package inflow

import (
	"context"
	"reflect"
	"testing"

	"github.com/FloMorphic/morph-api/models"
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
	return node.Extrinsic.OperationData
}

// The whole session config has to survive compilation: the handler cannot
// recover any of it from the flow at run time.
func TestBuildHitlNodeShipsSessionConfig(t *testing.T) {
	payload := buildHitl(t, map[string]any{
		"title":   "Ask a human",
		"mode":    "continue",
		"channel": "telegram",
		"prompt":  "Review {{$.llm.messages}} and answer.",
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
	// The prompt travels with its variables intact — the runtime resolves them
	// against the run's context on the way to the handler, so rewriting them here
	// would destroy exactly the mechanism that fills the session in.
	if payload["prompt"] != "Review {{$.llm.messages}} and answer." {
		t.Fatalf("prompt = %v", payload["prompt"])
	}
	// A HITL node has no question list to ship — what to ask is worked out in
	// the session — so one must never reach the handler from the canvas.
	if _, ok := payload["questions"]; ok {
		t.Fatalf("a question list was compiled into the payload: %+v", payload)
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

// The resume guards run before anything touches the store or the engine, so a
// task with nothing to resume is a quiet no-op rather than an error the UI would
// have to explain away when a person simply closes a session.
func TestResumeHumanTaskNoOps(t *testing.T) {
	nexts := []inflowModels.Next{{FlowId: "f1", NodeId: "n2"}}
	cases := []struct {
		name string
		task *models.HumanTask
	}{
		{name: "no task", task: nil},
		{
			// `continue` never stopped its flow — the run went on without waiting.
			name: "continue mode",
			task: &models.HumanTask{ID: "t1", FlowID: "f1", ContextID: "c1", Mode: models.HumanTaskContinue, Nexts: nexts},
		},
		{
			name: "parked at a node with no outbound edges",
			task: &models.HumanTask{ID: "t2", FlowID: "f1", ContextID: "c1", Mode: models.HumanTaskPark},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A nil store proves the guards return before any work is attempted.
			rec, err := ResumeHumanTask(context.Background(), nil, tc.task)
			if err != nil {
				t.Fatalf("ResumeHumanTask() error = %v, want nil", err)
			}
			if rec != nil {
				t.Fatalf("ResumeHumanTask() started run %+v, want none", rec)
			}
		})
	}
}

// A parked task that has somewhere to go but no run identity cannot be resumed,
// and saying so beats launching a run against an empty flow id.
func TestResumeHumanTaskRejectsTaskWithoutRunIdentity(t *testing.T) {
	task := &models.HumanTask{
		ID:    "t3",
		Mode:  models.HumanTaskPark,
		Nexts: []inflowModels.Next{{NodeId: "n2"}},
	}
	if _, err := ResumeHumanTask(context.Background(), nil, task); err == nil {
		t.Fatal("ResumeHumanTask() error = nil, want a missing flowId/contextId error")
	}
}

// A park that fanned out has to resume as every branch it had — collapsing to
// the first one silently drops the rest of the workflow.
func TestNextNodeIDsKeepsEveryBranch(t *testing.T) {
	got := nextNodeIDs([]inflowModels.Next{
		{NodeId: "a"},
		{NodeId: ""}, // an edge with no target is not an entry point
		{NodeId: "b"},
		{NodeId: "c"},
	})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nextNodeIDs() = %v, want %v", got, want)
	}
}

// A scheduled row is relaunched from its meta list; a row written before
// multi-node starts existed still relaunches from its single column.
func TestStartNodesFor(t *testing.T) {
	cases := []struct {
		name string
		rec  *models.Process
		want []string
	}{
		{
			name: "meta list wins",
			rec: &models.Process{
				StartNodeID: "a",
				// Read back from JSON, the list arrives as []any.
				Meta: map[string]any{MetaStartNodesKey: []any{"a", "b"}},
			},
			want: []string{"a", "b"},
		},
		{
			name: "in-memory list is accepted too",
			rec:  &models.Process{StartNodeID: "a", Meta: map[string]any{MetaStartNodesKey: []string{"a", "b"}}},
			want: []string{"a", "b"},
		},
		{
			name: "legacy row falls back to its column",
			rec:  &models.Process{StartNodeID: "solo"},
			want: []string{"solo"},
		},
		{
			name: "empty meta list falls back to the column",
			rec:  &models.Process{StartNodeID: "solo", Meta: map[string]any{MetaStartNodesKey: []any{}}},
			want: []string{"solo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := startNodesFor(tc.rec); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("startNodesFor() = %v, want %v", got, tc.want)
			}
		})
	}
}
