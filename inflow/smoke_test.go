package inflow

import (
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
)

func TestCompileMorphicNodes(t *testing.T) {
	// Plugin-backed nodes (llm/mcp/cast) need the inflow backend initialised to
	// resolve their infra account, so this smoke test exercises the non-plugin
	// lowerings (void / code / extrinsic) plus the promissall depends post-pass.
	flow := models.FlowRecord{
		ID: "flow_1",
		ViewFlow: compiler.VueFlow{
			Nodes: []compiler.VueFlowNode{
				{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
				{ID: "c", Type: NODE_JS, Data: map[string]any{
					"title": "JS", "key": "result", "scope": "$", "lang": "js", "logic_rule": "return {}",
				}},
				{ID: "d", Type: NODE_DOCSTORE, Data: map[string]any{
					"title": "Doc", "key": "docResult", "scope": "$", "action": "search", "storeId": "mem_1",
				}},
				{ID: "j", Type: NODE_PROMISEALL, Data: map[string]any{"title": "Join", "scope": "$"}},
			},
			Edges: []compiler.Edges{
				{ID: "e1", Source: "s", Target: "c"},
				{ID: "e2", Source: "c", Target: "d"},
				{ID: "e3", Source: "d", Target: "j"},
				{ID: "e4", Source: "s", Target: "j"},
			},
		},
	}

	start, nodes, err := FLowCompiler(flow)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if start != "s" {
		t.Fatalf("start = %q, want s", start)
	}

	if c := nodes["c"]; c == nil || c.Type != inflowModels.CodeNodeType {
		t.Fatalf("js node = %+v, want code type", c)
	}

	doc := nodes["d"]
	if doc == nil || doc.Type != inflowModels.ExtrinsicNodeType {
		t.Fatalf("docstore node = %+v, want extrinsic type", doc)
	}
	if doc.Extrinsic == nil || !strings.Contains(doc.Extrinsic.Subject, "svc.store.doc.search") {
		t.Fatalf("docstore subject = %q, want svc.store.doc.search", doc.Extrinsic.Subject)
	}

	join := nodes["j"]
	if join == nil || join.Type != inflowModels.VoidNodeType {
		t.Fatalf("promissall node = %+v, want void", join)
	}
	// Depends should include both inbound sources (start and docstore).
	if len(join.Depends) != 2 {
		t.Fatalf("promissall depends = %v, want the 2 inbound nodes", join.Depends)
	}
}
