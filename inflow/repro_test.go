package inflow

import (
	"testing"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

func makeFlow(nodes []compiler.VueFlowNode, edges []compiler.Edges) models.FlowRecord {
	return models.FlowRecord{ID: "flow_repro", ViewFlow: compiler.VueFlow{Nodes: nodes, Edges: edges}}
}

// Repro: start -> B, then add C, draw start -> C, remove start -> B.
// Final graph: start -> C only; B is an orphan.
func TestReproEdgeRewire(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			{ID: "b", Type: NODE_JS, Data: map[string]any{"title": "B", "key": "bkey", "scope": "$", "lang": "js", "logic_rule": "return {b:1}"}},
			{ID: "c", Type: NODE_JS, Data: map[string]any{"title": "C", "key": "ckey", "scope": "$", "lang": "js", "logic_rule": "return {c:1}"}},
		},
		[]compiler.Edges{
			{ID: "e_new", Source: "s", Target: "c"},
		},
	)

	start, nodes, err := FLowCompiler(flow)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	t.Logf("start=%s", start)
	for id, n := range nodes {
		lr := ""
		if n.Code != nil {
			lr = n.Code.LogicRule
		}
		t.Logf("node id=%q title=%q key=%q type=%v logic=%q next=%+v", id, n.Title, n.Key, n.Type, lr, n.Next)
	}

	c := nodes["c"]
	if c == nil {
		t.Fatalf("node c missing")
	}
	if c.Code == nil || c.Code.LogicRule != "return {c:1}" {
		t.Fatalf("node c has wrong logic: %+v", c.Code)
	}
	if c.Title != "C" || c.Key != "ckey" {
		t.Fatalf("node c identity crossed: title=%q key=%q", c.Title, c.Key)
	}
}
