package inflow

import (
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
)

/*
in front side we have a pallett and node types that in compile time those will map to inflow generics types
const items: PaletteItem[] = [

	// Extensions tab
		Feed through Call Api - Get Extrinsics

	// Generics tab
	{ type: 'startNode', title: 'Start', icon: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2z M12 16c-2.21 0-4-1.79-4-4s1.79-4 4-4 4 1.79 4 4-1.79 4-4 4z', tab: 'generics' },
	{ type: 'pluginNative', title: 'PluginNative', icon: 'M20.84 4.61a5.5 5.5 0 0 0-7.78 0L12 5.67l-1.06-1.06a5.5 5.5 0 1 0-7.78 7.78l1.06 1.06L12 21.23l7.78-7.78 1.06-1.06a5.5 5.5 0 0 0 0-7.78z', tab: 'generics' },
	{ type: 'code', title: 'Code', icon: 'M16 18l6-6-6-6 M8 6l-6 6 6 6', tab: 'generics' },
	{ type: 'contract', title: 'Contract', icon: 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2l6 6 M14 2v6h6', tab: 'generics' },
	{ type: 'extrinsics', title: 'Extrinsics', icon: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z', tab: 'generics' },
	{ type: 'goto', title: 'Goto', icon: 'M7 17L17 7 M7 7h10v10', tab: 'generics' },
	{ type: 'void', title: 'Void', icon: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2z M12 16c-2.21 0-4-1.79-4-4s1.79-4 4-4 4 1.79 4 4-1.79 4-4 4z', tab: 'generics' },

]
*/
const (
	// Legacy / generic inflow node kinds still handled by the compiler.
	// just void , contract, goto , nativley comes from inflow , others compiled to inflow native 
	// NODE_PLUGIN   = "pluginNative"
	NODE_VOID     = "void"
	NODE_CONTRACT = "contract"
	// NODE_CODE     = "code"
	// NODE_EXT_SVC  = "extrinsic"
	NODE_GOTO     = "goto"

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
