package inflow

import (
	"context"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// js is a code node, the one morphic kind that lowers without the inflow
// runtime being up — so these tests exercise the validation, not the engine.
func js(id, title string) compiler.VueFlowNode {
	return compiler.VueFlowNode{ID: id, Type: NODE_JS, Data: map[string]any{
		"title": title, "key": id, "scope": "$", "lang": "js", "logic_rule": "return {}",
	}}
}

func issueWith(d Diagnostics, code compiler.ValidationCode) *compiler.ValidationIssue {
	for i := range d.Issues {
		if d.Issues[i].Code == code {
			return &d.Issues[i]
		}
	}
	return nil
}

// A sound flow is clean: no issues, no counts, ok.
func TestCompileFlowClean(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			js("a", "A"),
			js("b", "B"),
			{ID: "j", Type: NODE_PROMISEALL, Data: map[string]any{"title": "Join", "scope": "$"}},
		},
		[]compiler.Edges{
			{ID: "e1", Source: "s", Target: "a"},
			{ID: "e2", Source: "s", Target: "b"},
			{ID: "e3", Source: "a", Target: "j"},
			{ID: "e4", Source: "b", Target: "j"},
		},
	)

	res := CompileFlow(flow, WithGuard())
	if !res.Diagnostics.OK || res.Diagnostics.ErrorCount != 0 || res.Diagnostics.WarningCount != 0 {
		t.Fatalf("diagnostics = %+v, want clean", res.Diagnostics)
	}
	if err := res.Diagnostics.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	// The join still waits on both branches: the post-pass runs before the guard.
	if join := res.Nodes["j"]; join == nil || len(join.Depends) != 2 {
		t.Fatalf("join = %+v, want depends on both branches", join)
	}
}

// The shape the fusion guard exists for: a loop back into a fan-in join grows
// the join's depends to include a node that only runs after it, so the first
// crossing deadlocks. The join's depends is edge-derived (the promissall
// post-pass), which is why the guard has to run after that pass — a guard
// inside Compile would see a join waiting for nothing and pass the graph.
func TestCompileFlowJoinDeadlock(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			js("a", "A"),
			{ID: "j", Type: NODE_PROMISEALL, Data: map[string]any{"title": "Join", "scope": "$"}},
			js("e", "After join"),
		},
		[]compiler.Edges{
			{ID: "e1", Source: "s", Target: "a"},
			{ID: "e2", Source: "a", Target: "j"},
			{ID: "e3", Source: "j", Target: "e"},
			// The back edge, wired into the join itself.
			{ID: "e_back", Source: "e", Target: "j"},
		},
	)

	diag := CompileFlow(flow, WithGuard()).Diagnostics
	if diag.OK || diag.ErrorCount == 0 {
		t.Fatalf("diagnostics = %+v, want a deadlock error", diag)
	}
	issue := issueWith(diag, compiler.CodeDependDeadlock)
	if issue == nil {
		t.Fatalf("issues = %+v, want a %s", diag.Issues, compiler.CodeDependDeadlock)
	}
	if issue.NodeId != "j" || issue.DependsOn != "e" {
		t.Fatalf("issue = %+v, want it to blame the join waiting on the node after it", issue)
	}
	// The edge to re-point and the targets it may point at instead are what a
	// canvas highlights, so they must survive the trip through Diagnostics.
	if issue.EdgeId != "e_back" {
		t.Fatalf("issue edgeId = %q, want e_back", issue.EdgeId)
	}
	if len(issue.SuggestedTargets) == 0 {
		t.Fatalf("issue = %+v, want suggested targets for the back edge", issue)
	}
	if err := diag.Err(); err == nil || !strings.Contains(err.Error(), "j") {
		t.Fatalf("Err() = %v, want it to name the blocking node", err)
	}
}

// The guard is opt-in: by default — which is what every path but a save uses —
// the same graph is lowered and no whole-graph verdict is drawn. Those verdicts
// describe one traversal, from the flow's start node, and a run does not have to
// be that traversal (a HITL resume and a Continue After enter mid-graph, on
// several nodes at once), so they must not reach a run.
func TestGuardIsOptInAndLoweringIsUnchanged(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			js("a", "A"),
			{ID: "j", Type: NODE_PROMISEALL, Data: map[string]any{"title": "Join", "scope": "$"}},
			js("after", "After join"),
			js("orphan", "Orphan"),
		},
		[]compiler.Edges{
			{ID: "e1", Source: "s", Target: "a"},
			{ID: "e2", Source: "a", Target: "j"},
			{ID: "e3", Source: "j", Target: "after"},
			{ID: "e_back", Source: "after", Target: "j"},
		},
	)

	// The save's pass, which asks for the guard, sees the deadlock and the orphan.
	if diag := CompileFlow(flow, WithGuard()).Diagnostics; diag.OK || diag.ErrorCount == 0 {
		t.Fatalf("validated pass = %+v, want the deadlock reported", diag)
	}

	// The default pass reports neither, and still lowers the whole graph — join
	// depends included, so the engine gets exactly what it got before.
	res := CompileFlow(flow)
	if !res.Diagnostics.OK || len(res.Diagnostics.Issues) != 0 {
		t.Fatalf("skip-guard pass = %+v, want no issues", res.Diagnostics)
	}
	if err := res.Diagnostics.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil — a run is never refused by the guard", err)
	}
	if join := res.Nodes["j"]; join == nil || len(join.Depends) != 2 {
		t.Fatalf("join = %+v, want it lowered with both dependencies", join)
	}
	if res.Nodes["after"] == nil {
		t.Fatal("the node past the join was not lowered")
	}

	// FLowCompiler — the wire and the run paths — is the skipping one.
	if _, _, err := FLowCompiler(flow); err != nil {
		t.Fatalf("FLowCompiler on a graph the guard rejects: %v, want it compiled", err)
	}
}

// A node that genuinely did not compile is still an error with the guard off:
// the walk stopped there, so the graph handed to the engine is missing steps,
// which is not a verdict about a traversal but a fact about the lowering.
func TestNodeFailuresAreReportedWithoutTheGuard(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			{ID: "p", Type: NODE_PLUGIN, Data: map[string]any{"title": "Unbound", "scope": "$"}},
		},
		[]compiler.Edges{{ID: "e1", Source: "s", Target: "p"}},
	)

	diag := CompileFlow(flow).Diagnostics
	if diag.OK || diag.ErrorCount == 0 {
		t.Fatalf("diagnostics = %+v, want the node failure reported", diag)
	}
	if diag.Issues[0].NodeId != "p" {
		t.Fatalf("issue = %+v, want it on the plugin node", diag.Issues[0])
	}
}

// A flow with no start marker cannot be compiled at all, and that is reported
// as an issue like any other — not as a bare error string.
func TestCompileFlowNoStartNode(t *testing.T) {
	flow := makeFlow([]compiler.VueFlowNode{js("a", "A")}, nil)

	diag := CompileFlow(flow, WithGuard()).Diagnostics
	if issueWith(diag, CodeStartNodeMissing) == nil {
		t.Fatalf("issues = %+v, want a %s", diag.Issues, CodeStartNodeMissing)
	}
	if diag.OK {
		t.Fatalf("diagnostics = %+v, want not ok", diag)
	}
}

// A node the walk never reaches is a warning: it compiles, it never runs, and
// an author mid-wiring must still be able to save.
func TestCompileFlowUnreachableNodeIsWarning(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			js("a", "A"),
			js("orphan", "Orphan"),
		},
		[]compiler.Edges{{ID: "e1", Source: "s", Target: "a"}},
	)

	diag := CompileFlow(flow, WithGuard()).Diagnostics
	issue := issueWith(diag, CodeNodeUnreachable)
	if issue == nil {
		t.Fatalf("issues = %+v, want a %s", diag.Issues, CodeNodeUnreachable)
	}
	if issue.NodeId != "orphan" || issue.Severity != compiler.SeverityWarning {
		t.Fatalf("issue = %+v, want a warning on the orphan", issue)
	}
	if !diag.OK || diag.ErrorCount != 0 {
		t.Fatalf("diagnostics = %+v, want ok with a warning", diag)
	}
	if err := diag.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil — warnings never gate", err)
	}
}

// A node whose builder refuses its data is an error naming that node, and the
// orphan warnings are suppressed: the walk stopped there, so everything past it
// is "unreachable" for a reason already reported.
func TestCompileFlowNodeBuildFailure(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			// A plugin node with no action has nothing to call.
			{ID: "p", Type: NODE_PLUGIN, Data: map[string]any{"title": "Unbound", "scope": "$"}},
			js("after", "After"),
		},
		[]compiler.Edges{
			{ID: "e1", Source: "s", Target: "p"},
			{ID: "e2", Source: "p", Target: "after"},
		},
	)

	diag := CompileFlow(flow, WithGuard()).Diagnostics
	issue := issueWith(diag, CodeNodeBuildFailed)
	if issue == nil {
		// Without a connected runtime the node fails earlier, on its infra
		// account — a different code, by design, since that is not the flow's
		// fault. Either way it must be an error naming the node.
		if issue = issueWith(diag, CodePluginAccountUnresolved); issue == nil {
			t.Fatalf("issues = %+v, want the unbound plugin node reported", diag.Issues)
		}
	}
	if issue.NodeId != "p" || issue.Severity != compiler.SeverityError {
		t.Fatalf("issue = %+v, want an error on the plugin node", issue)
	}
	if !strings.Contains(issue.Message, "Unbound") {
		t.Fatalf("message = %q, want it to name the node by its title", issue.Message)
	}
	if issueWith(diag, CodeNodeUnreachable) != nil {
		t.Fatalf("issues = %+v, want no orphan warnings behind a failed node", diag.Issues)
	}
}

// Diagnostics put errors before warnings and count both, so a client can render
// the list as it comes and branch on `ok`.
func TestDiagnosticsOrderAndCounts(t *testing.T) {
	var d Diagnostics
	d.add(compiler.ValidationIssue{Code: CodeNodeUnreachable, Severity: compiler.SeverityWarning, NodeId: "w"})
	d.add(compiler.ValidationIssue{Code: CodeNodeBuildFailed, Severity: compiler.SeverityError, NodeId: "e"})

	if d.Issues[0].Severity != compiler.SeverityError {
		t.Fatalf("issues = %+v, want the error first", d.Issues)
	}
	if d.ErrorCount != 1 || d.WarningCount != 1 || d.OK {
		t.Fatalf("diagnostics = %+v, want 1 error / 1 warning / not ok", d)
	}
	if got := d.Summary(); !strings.Contains(got, "1 error") {
		t.Fatalf("summary = %q, want it to count the error", got)
	}
}

// The old three-value contract still holds: the collapsed error is the gate,
// and a flow with warnings only compiles as it always did.
func TestFLowCompilerKeepsErrorContract(t *testing.T) {
	flow := makeFlow(
		[]compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			js("a", "A"),
			js("orphan", "Orphan"),
		},
		[]compiler.Edges{{ID: "e1", Source: "s", Target: "a"}},
	)

	start, nodes, err := FLowCompiler(flow)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if start != "s" || nodes["a"] == nil {
		t.Fatalf("start=%q nodes=%v, want the graph lowered", start, nodes)
	}

	_, _, err = FLowCompiler(models.FlowRecord{ID: "flow_empty"})
	if err == nil {
		t.Fatal("compiling a flow with no start node: want an error")
	}
}

// The launch paths a resume uses are not gated by the guard: a HITL resume
// enters at the nodes the parked node handed back — several of them, none the
// flow's start node — and a Continue After does the same after a delay. Both
// must get as far as the dispatch (which fails here, there being no engine in a
// test) rather than being refused over the graph.
func TestResumeLaunchIsNotRefusedForAnInvalidGraph(t *testing.T) {
	store := entryStore(t)
	ctx := context.Background()

	// A graph the editor's pass rejects: the loop back into the fan-in join.
	rec := models.FlowRecord{Title: "deadlocked", ViewFlow: compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{
			{ID: "s", Type: NODE_START, Data: map[string]any{"title": "Start", "scope": "$"}},
			js("a", "A"),
			{ID: "j", Type: NODE_PROMISEALL, Data: map[string]any{"title": "Join", "scope": "$"}},
			js("after", "After join"),
		},
		Edges: []compiler.Edges{
			{ID: "e1", Source: "s", Target: "a"},
			{ID: "e2", Source: "a", Target: "j"},
			{ID: "e3", Source: "j", Target: "after"},
			{ID: "e_back", Source: "after", Target: "j"},
		},
	}}
	if err := store.Workflows().Upsert(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	if !CompileFlow(rec, WithGuard()).Diagnostics.HasErrors() {
		t.Fatal("fixture is not an invalid graph any more")
	}
	cRec := &models.ContextRecord{Title: "run", Context: "{}"}
	if err := store.Contexts().Upsert(ctx, cRec); err != nil {
		t.Fatal(err)
	}

	// The HITL-resume shape: entry nodes pinned (two of them, mid-graph), the
	// entry form skipped because the context was seeded when the run started.
	proc, err := StartWorkflow(ctx, store, StartParams{
		FlowID:       rec.ID,
		ContextID:    cRec.ID,
		StartNodeIDs: []string{"j", "after"},
		EntrySkip:    true,
		RecordMeta:   map[string]any{"origin": "hitl_resume"},
	})
	if proc == nil {
		t.Fatalf("the resume was refused before a row was recorded: %v", err)
	}
	if got := startNodesFor(proc); len(got) != 2 {
		t.Fatalf("recorded start nodes = %v, want both resumed branches", got)
	}
	// The failure it does report is the missing engine, never the graph.
	if err != nil && strings.Contains(err.Error(), "depend") {
		t.Fatalf("resume refused over the depends guard: %v", err)
	}
}
