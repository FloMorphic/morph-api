package designer

import (
	"testing"

	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// findNodeByType returns the first planned node of a type, for assertions.
func findNodeByType(g compiler.VueFlow, typ string) (compiler.VueFlowNode, bool) {
	for _, n := range g.Nodes {
		if n.Type == typ {
			return n, true
		}
	}
	return compiler.VueFlowNode{}, false
}

func hasProblem(ps []Problem, level, needle string) bool {
	for _, p := range ps {
		if p.Level == level && contains(p.Message, needle) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A per-element LLM (no functions) feeding a rule that routes once — the correct
// shape. Checks ref→id wiring, scope preserved, message shorthand normalised,
// handler tags derived, and the edge routed on data.tags.
func TestPlanPatch_ScopedEnrichThenRoute(t *testing.T) {
	patch := Patch{
		Nodes: []PatchNode{
			{Ref: "start", Kind: "startNode", Title: "Start"},
			{
				Ref: "summarize", Kind: "llm", Title: "Summarize each issue",
				Scope: "$.issues[*]", Key: "issues",
				Data: map[string]any{
					"system": "You summarize issues.",
					"prompt": "Issue {{$this.id}}: {{$this.title}}\n\n{{$this.description}}",
				},
			},
			{
				Ref: "route", Kind: "rule", Title: "Route", Scope: "$",
				Data: map[string]any{
					"lang":       "js",
					"logic_rule": "let d = { pass: true }\nd",
					"handlers": []any{
						map[string]any{"name": "escalate", "title": "Escalate"},
						map[string]any{"name": "close", "title": "Close"},
					},
				},
			},
		},
		Edges: []PatchEdge{
			{From: "start", To: "summarize"},
			{From: "summarize", To: "route"},
			{From: "route", To: "start", Port: "escalate"}, // wire target is arbitrary here
		},
	}

	g, problems := PlanPatch(patch, nil)

	if len(g.Nodes) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(g.Nodes))
	}

	// The LLM node: scope preserved verbatim, and system/prompt folded into
	// body.messages (system first), with the {{$this...}} template untouched.
	llm, ok := findNodeByType(g, "llm")
	if !ok {
		t.Fatal("no llm node")
	}
	ld := llm.Data.(map[string]any)
	if ld["scope"] != "$.issues[*]" {
		t.Errorf("llm scope = %v, want $.issues[*]", ld["scope"])
	}
	if _, leaked := ld["prompt"]; leaked {
		t.Error("prompt shorthand not consumed into body.messages")
	}
	body := ld["body"].(map[string]any)
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[1]["role"] != "user" {
		t.Fatalf("messages not normalised system-then-user: %+v", msgs)
	}
	if msgs[1]["content"] != "Issue {{$this.id}}: {{$this.title}}\n\n{{$this.description}}" {
		t.Errorf("user message template altered: %v", msgs[1]["content"])
	}

	// The rule node: each handler stamped with an id and a tags list from its name.
	rule, _ := findNodeByType(g, "rule")
	handlers := rule.Data.(map[string]any)["handlers"].([]map[string]any)
	if len(handlers) != 2 {
		t.Fatalf("want 2 handlers, got %d", len(handlers))
	}
	if id, _ := handlers[0]["id"].(string); id == "" {
		t.Error("handler 0 got no id")
	}
	tags0 := handlers[0]["tags"].([]string)
	if len(tags0) != 1 || tags0[0] != "escalate" {
		t.Errorf("handler 0 tags = %v, want [escalate]", tags0)
	}

	// The routed edge leaving the rule carries the handler's tag on data.tags —
	// what the inflow-fusion compiler routes on.
	var routed *compiler.Edges
	for i := range g.Edges {
		if g.Edges[i].Source == rule.ID {
			routed = &g.Edges[i]
		}
	}
	if routed == nil {
		t.Fatal("no edge leaving the rule node")
	}
	if len(routed.Data.Tags) != 1 || routed.Data.Tags[0] != "escalate" {
		t.Errorf("routed edge tags = %v, want [escalate]", routed.Data.Tags)
	}

	// This is the correct shape — no design problems expected.
	for _, p := range problems {
		if p.Level == "error" {
			t.Errorf("unexpected error problem: %s", p.Message)
		}
	}
}

// The bad design the user reported: an LLM that routes (bound functions) given a
// wildcard scope, then a promissall after a single scoped node. Both must be
// flagged.
func TestPlanPatch_ManyScopeOnRoutingNodeFlagged(t *testing.T) {
	patch := Patch{
		Nodes: []PatchNode{
			{Ref: "start", Kind: "startNode", Title: "Start"},
			{
				Ref: "triage", Kind: "llm", Title: "Triage issue", Scope: "$.issues[*]",
				Data: map[string]any{
					"functions": []any{
						map[string]any{"name": "escalate", "description": "when urgent"},
						map[string]any{"name": "ignore", "description": "when noise"},
					},
				},
			},
		},
		Edges: []PatchEdge{
			{From: "start", To: "triage"},
			{From: "triage", To: "start", Port: "escalate"},
		},
	}

	_, problems := PlanPatch(patch, nil)
	if !hasProblem(problems, "warn", "routes on its result but has a many-valued scope") {
		t.Errorf("expected many-scope warning, got %+v", problems)
	}
}

// The exact pair of bugs reported from Claude Desktop: a scoped LLM whose user
// prompt addresses rows by fixed root ({{$.title}}) instead of {{$this.title}},
// followed by a promissall that only one edge reaches. Both must surface in
// problems — the signals that were silent before.
func TestPlanPatch_RowRefAndNoopPromissallFlagged(t *testing.T) {
	patch := Patch{
		Nodes: []PatchNode{
			{Ref: "start", Kind: "startNode", Title: "Start"},
			{
				Ref: "review", Kind: "llm", Title: "Review issue", Scope: "$.issues[*]", Key: "issues",
				Data: map[string]any{
					"prompt": "Issue #{{$.index}} (id: {{$.id}})\nTitle: {{$.title}}\nDescription:\n{{$.description}}",
				},
			},
			{Ref: "join", Kind: "promissall", Title: "Wait for every issue"},
		},
		Edges: []PatchEdge{
			{From: "start", To: "review"},
			{From: "review", To: "join"}, // single edge into the Wait for All
		},
	}

	_, problems := PlanPatch(patch, nil)

	if !hasProblem(problems, "warn", "never use \"{{$this…}}\"") {
		t.Errorf("expected $this row-ref warning, got %+v", problems)
	}
	if !hasProblem(problems, "warn", "nothing to join and is a no-op") {
		t.Errorf("expected no-op promissall warning, got %+v", problems)
	}
}

// A scoped node that DOES use $this raises no row-ref warning (no false positive).
func TestPlanPatch_RowRefWithThisIsClean(t *testing.T) {
	patch := Patch{
		Nodes: []PatchNode{
			{Ref: "start", Kind: "startNode", Title: "Start"},
			{Ref: "review", Kind: "llm", Title: "Review", Scope: "$.issues[*]",
				Data: map[string]any{"prompt": "Issue {{$this.id}}: {{$this.title}}"}},
		},
		Edges: []PatchEdge{{From: "start", To: "review"}},
	}
	_, problems := PlanPatch(patch, nil)
	for _, p := range problems {
		if contains(p.Message, "{{$this…}}") {
			t.Errorf("false-positive row-ref warning: %s", p.Message)
		}
	}
}

// A loop topology (init → guard rule → body → advance → back-edge to the guard)
// must NOT trip the "N branches arrive, runs N times" convergence warning: the
// guard's second inbound edge is a back-edge (sequential re-entry), not a fork.
func TestPlanPatch_LoopBackEdgeNotFlaggedAsConvergence(t *testing.T) {
	patch := Patch{
		Nodes: []PatchNode{
			{Ref: "start", Kind: "startNode", Title: "Start"},
			{Ref: "init", Kind: "js", Title: "Init i", Scope: "$", Key: "i",
				Data: map[string]any{"lang": "js", "logic_rule": "let i = 0\ni"}},
			{Ref: "guard", Kind: "rule", Title: "More items?", Scope: "$",
				Data: map[string]any{"lang": "js", "logic_rule": "let d = {}\nd",
					"handlers": []any{
						map[string]any{"name": "next"},
						map[string]any{"name": "done"},
					}}},
			{Ref: "body", Kind: "js", Title: "Process item", Scope: "$", Key: "current",
				Data: map[string]any{"lang": "js", "logic_rule": "let current = input.items[input.i]\ncurrent"}},
			{Ref: "advance", Kind: "js", Title: "i++", Scope: "$", Key: "i",
				Data: map[string]any{"lang": "js", "logic_rule": "let i = input.i + 1\ni"}},
			{Ref: "after", Kind: "js", Title: "After loop", Scope: "$", Key: "out",
				Data: map[string]any{"lang": "js", "logic_rule": "let out = true\nout"}},
		},
		Edges: []PatchEdge{
			{From: "start", To: "init"},
			{From: "init", To: "guard"},
			{From: "guard", To: "body", Port: "next"},
			{From: "body", To: "advance"},
			{From: "advance", To: "guard"}, // back-edge closing the loop
			{From: "guard", To: "after", Port: "done"},
		},
	}

	_, problems := PlanPatch(patch, nil)
	for _, p := range problems {
		if contains(p.Message, "branches arrive at") {
			t.Errorf("loop back-edge wrongly flagged as convergence: %s", p.Message)
		}
	}
}

// An unknown port name on a derived-port source drops the edge with an error
// listing the real ports.
func TestPlanPatch_UnknownPortDropsEdge(t *testing.T) {
	patch := Patch{
		Nodes: []PatchNode{
			{Ref: "start", Kind: "startNode", Title: "Start"},
			{Ref: "r", Kind: "rule", Title: "R", Data: map[string]any{
				"handlers": []any{map[string]any{"name": "yes"}},
			}},
		},
		Edges: []PatchEdge{
			{From: "r", To: "start", Port: "nope"},
		},
	}
	g, problems := PlanPatch(patch, nil)
	if len(g.Edges) != 0 {
		t.Errorf("edge with unknown port should be dropped, got %d edges", len(g.Edges))
	}
	if !hasProblem(problems, "error", `no port "nope"`) {
		t.Errorf("expected unknown-port error, got %+v", problems)
	}
}
