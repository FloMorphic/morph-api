package inflow

import (
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
)

const (
	// Legacy / generic inflow node kinds still handled by the compiler.
	// just void , contract, goto , nativley comes from inflow , others compiled to inflow native
	NODE_VOID     = "void"
	NODE_CONTRACT = "contract"
	NODE_GOTO = "goto"

	// FloMorphic builtin morphic types (see models/extension.go). Each lowers to
	// an inflow primitive below.
	NODE_START      = "startNode"  // -> void (start marker)
	NODE_HITL       = "hitl"       // -> extrinsic (svc.hitl.add)
	NODE_DOCSTORE   = "docstore"   // -> extrinsic (svc.store.doc.{ACTION})
	NODE_VECSTORE   = "vecstore"   // -> extrinsic (svc.store.vec.{ACTION})
	NODE_PROMISEALL = "promissall" // -> void (depends on all inbound nodes)
	NODE_LLM        = "llm"        // -> plugin
	NODE_MCP        = "mcp"        // -> plugin (MCP client)
	NODE_RULE       = "rule"       // -> contract
	NODE_JS         = "js"         // -> code (variant js)
	NODE_OPA        = "opa"        // -> code (variant opa)
	NODE_UNTIL      = "until"      // -> extrinsic (svc.continue.at)
	NODE_CAST       = "cast"       // -> plugin
	NODE_HTTP       = "http"       // -> plugin (HTTP request client)

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
func FLowCompiler(f models.FlowRecord) (string, map[string]*inflowModels.Node, error) {
	startNodeId, err := GetStartNodeId(f)
	if err != nil {
		return startNodeId, nil, err
	}
	cmpr := compiler.NewVueFlowCompiler(compiler.WithEachNodeFunc(NodeBuilder))
	if cmpr == nil {
		return startNodeId, nil, fmt.Errorf("error occurred in compile process")
	}
	l, errs := cmpr.Compile(startNodeId, f.ViewFlow)
	for _, e := range errs {
		return startNodeId, l, e
	}

	// Post-pass — edge-derived wiring the per-node hook cannot see (it only gets
	// one node): a promissall (fan-in) waits on every inbound node.
	//
	// A `until` node's outbound "next nodes" are intentionally NOT resolved here:
	// they must be shipped in the inflow model shape, which the continue.at
	// listener recovers from the request body at run time (not from the vueFlow
	// graph). See the svc handler in port.go.
	for _, n := range f.ViewFlow.Nodes {
		cn, ok := l[n.ID]
		if !ok || cn == nil {
			continue
		}
		if n.Type == NODE_PROMISEALL {
			cn.Depends = inboundSources(f.ViewFlow, n.ID)
		}
	}

	return startNodeId, l, nil
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
	node := inflowModels.Node{
		ID:    vfn.ID,
		Title: nodeData["title"].(string),
	}
	if nodeData["key"] != nil {
		node.Key = nodeData["key"].(string)
	}
	if nodeData["scope"] != nil {
		node.Scope = nodeData["scope"].(string)

	}
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
