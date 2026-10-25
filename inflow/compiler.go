package inflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
)

const (
	// Legacy / generic inflow node kinds still handled by the compiler.
	// just void , contract, goto , nativley comes from inflow , others compiled to inflow native
	NODE_VOID     = "void"
	NODE_CONTRACT = "contract"
	NODE_GOTO     = "goto"

	// FloMorphic builtin morphic types (see models/extension.go). Each lowers to
	// an inflow primitive below.
	NODE_START      = "startNode"   // -> void (start marker)
	NODE_HITL       = "hitl"        // -> extrinsic (svc.hitl.add)
	NODE_DOCSTORE   = "docstore"    // -> extrinsic (svc.store.doc.{ACTION})
	NODE_VECSTORE   = "vecstore"    // -> extrinsic (svc.store.vec.{ACTION})
	NODE_PROMISEALL = "promissall"  // -> void (depends on all inbound nodes)
	NODE_LLM        = "llm"         // -> plugin
	NODE_MCP        = "mcp"         // -> plugin (MCP client)
	NODE_RULE       = "rule"        // -> contract
	NODE_JS         = "js"          // -> code (variant js)
	NODE_OPA        = "opa"         // -> code (variant opa)
	NODE_UNTIL      = "until"       // -> extrinsic (svc.continue.at)
	NODE_CAST       = "cast"        // -> plugin
	NODE_HTTP       = "http"        // -> plugin (HTTP request client)
	NODE_DECISION   = "ai-decision" // -> plugin (decision model over either protocol: System One — Jev / Laya / nimble — or the Decisions API)

	// NODE_JEV is what this node's kind used to be called, when it only spoke
	// to TypeSafe's Jev. The protocol it speaks (POST /v1/systemone) is served
	// by other models too — Laya, local-first and open — so the kind was
	// renamed to the capability rather than the vendor. Flows saved under the
	// old kind still compile: it lowers through the same builder.
	NODE_JEV = "jev"

	// NODE_PLUGIN is a node contributed by an imported inflowv1 plugin: one
	// action of that plugin, dropped from the palette after a sync. Unlike the
	// builtins above it has no hard-coded contract — the plugin's own action
	// form decides its fields — so it lowers generically.
	NODE_PLUGIN = "plugin" // -> plugin (any imported plugin action)

	// Store service subject templates (inflo-fusion listens on svc.store.{doc,vec}.*).
	SubjectStoreDoc = "svc.store.doc.{ACTION}"
	SubjectStoreVec = "svc.store.vec.{ACTION}"
	// ContinueSubject records/parks a run to resume its outbound nodes at a time.
	ContinueSubject = "svc.continue.at"
)

func GetStartNodeId(f models.FlowRecord) (string, error) {
	startNodeId := ""
	for _, n := range f.ViewFlow.Nodes {
		if n.Type == NODE_START {
			startNodeId = n.ID
			break
		}
	}
	if startNodeId == "" {
		return startNodeId, fmt.Errorf("start node is required")
	}
	return startNodeId, nil
}

// CompileResult is one compile pass over a saved flow: the lowered graph plus
// everything the pass found wrong with it (see Diagnostics). It never fails —
// a flow that cannot be compiled comes back with error-severity issues and
// whatever was lowered before the walk stopped, so a caller can report the
// mistakes instead of a single string.
//
// Nothing here is a gate. A caller that genuinely needs the graph complete asks
// Diagnostics.CompileErr(), which answers "did this lower" and leaves out every
// guard verdict — those are only ever shown.
type CompileResult struct {
	StartNodeID string
	Nodes       map[string]*inflowModels.Node
	Diagnostics Diagnostics
}

// CompileOption tunes one compile pass. See WithGuard.
type CompileOption func(*compileOptions)

type compileOptions struct{ guard bool }

// WithGuard adds the whole-graph validation to a compile pass: the depends
// guard (a node may not wait for a step that comes after it) and the
// reachability warnings. Without it — the default — a flow is only lowered, and
// the sole thing reported is a node that genuinely did not compile.
//
// It is off by default, in this compiler and in fusion's, because those
// verdicts are statements about ONE traversal — the one that starts at the
// flow's `startNode` — and a run does not have to be that traversal. A HITL
// resume and a Continue After enter at the nodes the parked node handed back
// (several of them, mid-graph), a partial run enters at a node the operator
// picked, and on a resume the engine seeds the earlier run's traversal
// snapshot, so a join the guard calls deadlocked may be exactly the join that
// is already satisfied.
//
// So it is asked for where a flow is SAVED — with the author in front of the
// canvas, where naming the node and the edge to change is help — and the
// findings are never a refusal: nothing in this API turns a guard verdict into
// a failed request or a blocked run.
func WithGuard() CompileOption {
	return func(o *compileOptions) { o.guard = true }
}

// CompileFlow lowers a flow's Vue-Flow graph to the inflow node graph, and with
// WithGuard also validates the result (see Diagnostics).
//
// Validation is worth asking for where a flow is saved rather than where it is
// run: the compiled graph is otherwise only produced on the wire, when a running
// process asks for it (see InflowWire.RetrieveFlow), where a mistake has nowhere
// to go but a log line. On save the author is still looking at the canvas, and
// every issue names the node and the edge to change.
//
// The guard is also run here by hand rather than through fusion's own
// WithDependsGuard, and AFTER the promissall post-pass below. It has to be: a
// fan-in node's `depends` is edge-derived and only exists after that pass, so a
// guard running inside Compile would validate every join as if it waited for
// nothing — which is precisely the node the deadlock rule is about.
func CompileFlow(f models.FlowRecord, opts ...CompileOption) CompileResult {
	var o compileOptions
	for _, apply := range opts {
		apply(&o)
	}

	// A clean pass reports an empty issue list, not a null one: every consumer
	// of this iterates `issues` and branches on `ok`.
	res := CompileResult{
		Nodes:       map[string]*inflowModels.Node{},
		Diagnostics: Diagnostics{OK: true, Issues: []compiler.ValidationIssue{}},
	}

	startNodeId, err := GetStartNodeId(f)
	if err != nil {
		res.Diagnostics.add(compiler.ValidationIssue{
			Code:       CodeStartNodeMissing,
			Severity:   compiler.SeverityError,
			Message:    err.Error(),
			Suggestion: "add a Start node and connect it to the first step of the flow.",
		})
		return res
	}
	res.StartNodeID = startNodeId
	res.Diagnostics.StartNodeID = startNodeId

	// Builder refusals are recorded by the hook, not read off Compile's error
	// map: the walk returns a nested node's error up to its root, and Compile
	// files that under the START node's id — so a flow whose fifth node is
	// unbound would be reported as a broken Start. The hook knows which node it
	// was called for, so the attribution is taken from there and the node's own
	// error is still returned, leaving the walk's behaviour (it stops there)
	// exactly as it was.
	buildErrs := map[string]error{}
	hook := func(vfn compiler.VueFlowNode) (*inflowModels.Node, error) {
		node, err := NodeBuilder(vfn)
		if err != nil {
			buildErrs[vfn.ID] = err
		}
		return node, err
	}
	// No fusion guard option: it is off by default there too, and the guard this
	// pass runs happens further down, after the post-pass (see the doc comment).
	cmpr := compiler.NewVueFlowCompiler(compiler.WithEachNodeFunc(hook))
	nodes, compileErrs := cmpr.Compile(startNodeId, f.ViewFlow)
	if nodes == nil {
		nodes = map[string]*inflowModels.Node{}
	}
	res.Nodes = nodes
	// Anything the compiler reported that no builder call owned (it has its own
	// failure modes, and may grow more) is kept under the id it came with.
	for nodeId, err := range compileErrs {
		if _, ok := buildErrs[nodeId]; !ok && len(buildErrs) == 0 {
			buildErrs[nodeId] = err
		}
	}

	// Builder refusals, in node-id order so two compiles of the same graph
	// report the same list in the same order.
	for _, nodeId := range sortedKeys(buildErrs) {
		res.Diagnostics.add(buildIssue(f, nodeId, buildErrs[nodeId]))
	}

	// Post-pass — edge-derived wiring the per-node hook cannot see (it only gets
	// one node): a promissall (fan-in) waits on every inbound node.
	//
	// A `until` node's outbound "next nodes" are intentionally NOT resolved here:
	// they must be shipped in the inflow model shape, which the continue.at
	// listener recovers from the request body at run time (not from the vueFlow
	// graph). See the svc handler in port.go.
	for _, n := range f.ViewFlow.Nodes {
		cn, ok := nodes[n.ID]
		if !ok || cn == nil {
			continue
		}
		if n.Type == NODE_PROMISEALL {
			cn.Depends = inboundSources(f.ViewFlow, n.ID)
		}
	}

	if !o.guard {
		return res
	}

	// The engine's own guard over the finished graph: a node may not wait for a
	// step that comes after it (every code and the reasoning are in the fusion
	// compiler's docs). Its issues already name the node, the offending depends
	// entry, the edge to re-point and the ids it may legally target instead.
	res.Diagnostics.add(compiler.ValidateDepends(startNodeId, nodes).Issues...)

	// Nodes the walk never reached. Skipped when a builder refused a node: the
	// walk stops at such a node, so everything past it is "unreachable" for a
	// reason the author has already been told about.
	if len(buildErrs) == 0 {
		res.Diagnostics.add(unreachableIssues(f, nodes)...)
	}

	return res
}

// FLowCompiler is CompileFlow for the callers that only want the lowered graph
// and one error, with no guard (see WithGuard): the error is a node that
// genuinely did not compile, exactly as it was before the guard existed. It is
// what the engine wire and the run paths use — a running flow is never refused
// over a whole-graph verdict about a traversal it is not making.
func FLowCompiler(f models.FlowRecord) (string, map[string]*inflowModels.Node, error) {
	res := CompileFlow(f)
	return res.StartNodeID, res.Nodes, res.Diagnostics.Err()
}

// buildIssue turns one builder refusal into an issue. A plugin node that could
// not mint its infra account gets its own code: the graph is fine, the runtime
// is not connected, and telling an author to fix their flow would be wrong.
func buildIssue(f models.FlowRecord, nodeId string, err error) compiler.ValidationIssue {
	issue := compiler.ValidationIssue{
		Code:     CodeNodeBuildFailed,
		Severity: compiler.SeverityError,
		NodeId:   nodeId,
		Message:  fmt.Sprintf("%s could not be compiled: %v", nodeLabel(f, nodeId), err),
	}
	if errors.Is(err, ErrPluginAccount) {
		issue.Code = CodePluginAccountUnresolved
		issue.Message = fmt.Sprintf("%s could not be compiled: %v", nodeLabel(f, nodeId), err)
		issue.Suggestion = "this is the inflow runtime being unreachable, not a mistake in the flow: check the engine connection and try again."
		return issue
	}
	issue.Suggestion = "open the node and complete what it is missing — the steps after it are not compiled while it fails."
	return issue
}

// unreachableIssues reports the nodes of the graph the walk never reached from
// the start node: they are saved, they compile, and they never run.
func unreachableIssues(f models.FlowRecord, nodes map[string]*inflowModels.Node) []compiler.ValidationIssue {
	out := []compiler.ValidationIssue{}
	for _, n := range f.ViewFlow.Nodes {
		if _, ok := nodes[n.ID]; ok {
			continue
		}
		out = append(out, compiler.ValidationIssue{
			Code:     CodeNodeUnreachable,
			Severity: compiler.SeverityWarning,
			NodeId:   n.ID,
			Message: fmt.Sprintf("%s is not reachable from the start node, so it never runs",
				nodeLabel(f, n.ID)),
			Suggestion: "connect it to the flow, or remove it.",
		})
	}
	return out
}

// nodeLabel names a node the way a message should: by its title when it has one,
// always with its kind, so a reader recognises it on the canvas.
func nodeLabel(f models.FlowRecord, nodeId string) string {
	for _, n := range f.ViewFlow.Nodes {
		if n.ID != nodeId {
			continue
		}
		title := ""
		if data, ok := n.Data.(map[string]any); ok {
			title = strings.TrimSpace(getStr(data, "title"))
		}
		if title != "" {
			return fmt.Sprintf("node %q (%s)", title, n.Type)
		}
		return fmt.Sprintf("node %q", n.Type)
	}
	return fmt.Sprintf("node %s", nodeId)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// inboundSources returns the ids of every node with an edge into nodeId.
func inboundSources(flow compiler.VueFlow, nodeId string) []string {
	out := []string{}
	for _, e := range flow.Edges {
		if e.Target == nodeId {
			out = append(out, e.Source)
		}
	}
	return out
}

// NodeBuilder is the per-node compile hook: it scaffolds the inflow node
// (id / title / key / scope) and dispatches the type-specific population to the
// buildXxx functions in node_builders.go.
func NodeBuilder(vfn compiler.VueFlowNode) (*inflowModels.Node, error) {

	nodeData, ok := vfn.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid node data ")
	}
	// Read defensively (getStr). A graph reaching the compiler is not always
	// one the editor drew: a document posted to /flow/import or handed to an
	// MCP tool can name a node without a title, and an unchecked type
	// assertion here panicked the request handler rather than returning an
	// error the caller could read.
	node := inflowModels.Node{
		ID:    vfn.ID,
		Title: getStr(nodeData, "title"),
	}
	node.Key = getStr(nodeData, "key")
	node.Scope = getStr(nodeData, "scope")
	var err error
	switch vfn.Type {
	case NODE_START, NODE_PROMISEALL, NODE_VOID:
		// Start marker, the fan-in join and void all lower to a Void node. A
		// promissall's `depends` (all inbound nodes) is filled by a post-pass in
		// FLowCompiler, which has the flow's edges.
		node.Type = inflowModels.VoidNodeType
	case NODE_JS, NODE_OPA:
		err = buildCodeNode(&node, vfn, nodeData)
	case NODE_CONTRACT, NODE_RULE:
		err = buildRuleNode(&node, vfn, nodeData)
	case NODE_GOTO:
		err = buildGotoNode(&node, vfn, nodeData)
	case NODE_LLM:
		err = buildLLMNode(&node, vfn, nodeData)
	case NODE_MCP:
		err = buildMcpNode(&node, vfn, nodeData)
	case NODE_CAST:
		err = buildCastNode(&node, vfn, nodeData)
	case NODE_HTTP:
		err = buildHTTPNode(&node, vfn, nodeData)
	case NODE_DECISION, NODE_JEV:
		err = buildDecisionNode(&node, vfn, nodeData)
	case NODE_PLUGIN:
		err = buildPluginActionNode(&node, vfn, nodeData)
	case NODE_HITL:
		err = buildHitlNode(&node, vfn, nodeData)
	case NODE_DOCSTORE, NODE_VECSTORE:
		err = buildStoreNode(&node, vfn, nodeData)
	case NODE_UNTIL:
		err = buildUntilNode(&node, vfn, nodeData)
	}
	if err != nil {
		return nil, err
	}

	return &node, nil
}
