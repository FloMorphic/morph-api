package inflow

import (
	"fmt"
	"time"

	"github.com/FloMorphic/morph-api/models"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	inflowNodes "github.com/Inflowenger/inflow-fusion/nodes"
	"github.com/Inflowenger/inflow-fusion/svcHandler"
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
	NODE_PLUGIN   = "pluginNative"
	NODE_VOID     = "void"
	NODE_CONTRACT = "contract"
	NODE_CODE     = "code"
	NODE_EXT_SVC  = "extrinsic"
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

// getStr reads a string field defensively (empty string when absent/other type).
func getStr(data map[string]any, key string) string {
	if v, ok := data[key].(string); ok {
		return v
	}
	return ""
}

// getFloat reads a numeric field defensively. JSON-decoded node data carries
// numbers as float64; a string is parsed as a fallback (empty/other type → 0).
func getFloat(data map[string]any, key string) float64 {
	switch v := data[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return 0
}

// delayModeSeconds maps a Continue After delay unit to seconds. An unknown mode
// (including the "at" absolute mode and legacy "delay") returns 0.
func delayModeSeconds(mode string) int64 {
	switch mode {
	case "seconds":
		return 1
	case "minutes":
		return 60
	case "hour":
		return 3600
	case "day":
		return 86400
	}
	return 0
}

// parseAtMillis parses a Continue After absolute "at" value into epoch millis,
// accepting the browser datetime-local shape ("2006-01-02T15:04"[":05"]) and
// full RFC3339. It returns 0 when the value is blank or unparseable, leaving the
// handler to fall back to (or surface) a missing schedule.
func parseAtMillis(at string) int64 {
	if at == "" {
		return 0
	}
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"}
	for _, l := range layouts {
		if t, err := time.Parse(l, at); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// pluginUniqId is the identity a plugin node is registered under. It must match
// the backing extension-table row (stamped onto the node data as `extensionId`
// by the front end when a node is dropped from the palette). Falls back to the
// node's own id so a standalone / unstamped node still gets a stable identity.
func pluginUniqId(data map[string]any, nodeID string) string {
	if id := getStr(data, "extensionId"); id != "" {
		return id
	}
	return nodeID
}

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
	switch vfn.Type {
	case NODE_START, NODE_PROMISEALL:
		// Start marker and the fan-in join both lower to a Void node. A
		// promissall's `depends` (all inbound nodes) is filled by a post-pass in
		// FLowCompiler, which has the flow's edges.
		node.Type = inflowModels.VoidNodeType
	case NODE_JS, NODE_OPA:
		// JS / OPA nodes both lower to a Code node; the `lang` field on the data
		// selects the variant (js / opa).
		node.Type = inflowModels.CodeNodeType

		if lang, ok := nodeData["lang"].(string); ok {
			if lang == string(inflowModels.JavaScriptLang) {
				newJsNode := inflowNodes.NewJsNode(nodeData["logic_rule"].(string))
				node.Code = &newJsNode.CodeRule
			} else if lang == string(inflowModels.OPALang) {
				criteria := map[string]any{}
				if conds, ok := nodeData["conditions"].([]any); ok {
					for _, el := range conds {
						if field, ok := el.(map[string]any); ok {
							criteria[field["key"].(string)] = field["value"]
						}
					}
				}
				newOpaNode := inflowNodes.NewOpaNode(
					nodeData["logic_rule"].(string),
					nodeData["opa_result"].(string),
					inflowNodes.WithCriteriaData(criteria),
				)
				node.Code = &newOpaNode.CodeRule
			}

		}

	case NODE_CONTRACT, NODE_RULE:
		// Rule → a Contract; its handlers drive the routed outbound edges.
		node.Type = inflowModels.RuleNodeType

		criteria := map[string]any{}
		if conds, ok := nodeData["conditions"].([]any); ok {
			for _, el := range conds {
				if field, ok := el.(map[string]any); ok {
					criteria[field["key"].(string)] = field["value"]
				}
			}
		}
		if lang, ok := nodeData["lang"].(string); ok {
			if lang == string(inflowModels.JavaScriptLang) { //js lang
				newContract := inflowNodes.NewJsRuleLogicNode(
					inflowNodes.WithContractLogicCode(nodeData["logic_rule"].(string)),
					inflowNodes.WithContractConditions(criteria),
				)
				node.Contract = &newContract.ContractRule
			} else if lang == string(inflowModels.OPALang) { // opa-reo lang
				newContract := inflowNodes.NewOpaRuleLogicNode(nodeData["opa_result"].(string),
					inflowNodes.WithContractLogicCode(nodeData["logic_rule"].(string)),
					inflowNodes.WithContractConditions(criteria),
				)
				node.Contract = &newContract.ContractRule

			}
		}

	case NODE_GOTO:
		node.Type = inflowModels.GoToNodeType
		gotoNode := inflowNodes.NewGotoNode()
		if targetFlow, ok := nodeData["goto"].(map[string]any); ok {
			gotoNode.From(targetFlow["flowId"].(string), targetFlow["from_nodeId"].(string))
			gotoNode.To(targetFlow["flowId"].(string), targetFlow["end_nodeId"].(string))

		}
		node.GoTo = &gotoNode.GoToRule
	case NODE_PLUGIN, NODE_LLM, NODE_MCP, NODE_CAST:
		// pluginNative and the plugin-backed morphic nodes (LLM / MCP / Cast) all
		// lower to a Plugin node. The morphic nodes carry their extra config
		// (functions, url/auth, mappings, …) inside the plugin body so it travels
		// to the plugin at run time.
		node.Type = inflowModels.PluginNodeType
		// The plugin's uniqId must equal the backing extension-table row; the front
		// end stamps that id onto the node data as `extensionId` when the node is
		// dropped from the palette (see pluginUniqId).
		pluginNode, err := inflowNodes.NewPluginNode(
			getStr(nodeData, "title"),
			inflowNodes.WithUniqId[*inflowNodes.PluginNode](pluginUniqId(nodeData, vfn.ID)),
			inflowNodes.WithIdleWaitMinutes(int8(15)),
		)
		if err != nil {
			return nil, err
		}
		body, _ := nodeData["body"].(map[string]any)
		if body == nil {
			body = map[string]any{}
		}
		// Carry node-kind-specific config into the plugin body.
		for _, k := range []string{"functions", "url", "auth", "transport", "mappings", "storeId", "mcpMode"} {
			if v, ok := nodeData[k]; ok {
				body[k] = v
			}
		}
		// request / body come from the front end; default the request verb to "run".
		request := getStr(nodeData, "request")
		if request == "" {
			request = "run"
		}
		pluginNode.Body = body
		pluginNode.Request = request

		node.Plugin = &pluginNode.PluginRule
	case NODE_HITL:
		// Human-in-the-Loop → an extrinsic call to this backend's `hitl` svc.
		// The subject carries the nodeId so the handler can recover it; the
		// Data payload carries the title/questions to record on the task.
		node.Type = inflowModels.ExtrinsicNodeType
		subject := svcHandler.GetSvc(SvcHitl)
		if subject == "" {
			subject = svcHandler.SvcTopic(HitlSubject)
		}
		// The nodeId travels as the node's uniqId; the Data carries what the hitl
		// handler records on the task (title / questions).
		evNode := inflowNodes.NewExtrinsicSvcNode(string(subject), inflowNodes.WithUniqId[*inflowNodes.ExtrinsicSvcNode](vfn.ID))
		evNode.ExtrinsicRule.ReqTimeoutSecound = 10
		payload := map[string]any{"nodeId": vfn.ID}
		if nodeData["title"] != nil {
			payload["title"] = nodeData["title"]
		}
		if nodeData["questions"] != nil {
			payload["questions"] = nodeData["questions"]
		}
		evNode.ExtrinsicRule.Data = payload
		node.Extrinsic = &evNode.ExtrinsicRule
	case NODE_DOCSTORE, NODE_VECSTORE:
		// Doc / Vector store → an extrinsic on svc.store.{doc,vec}.{ACTION}. The
		// referenced store travels in the Data payload; the action selects the
		// concrete subject. A `read` carries its `query` (SQL run against the
		// store); a `write` carries its `input` (the JSONPath/scope of the value
		// to store).
		node.Type = inflowModels.ExtrinsicNodeType
		action := getStr(nodeData, "action")
		if action == "" {
			action = "read"
		}
		tmpl := SubjectStoreDoc
		if vfn.Type == NODE_VECSTORE {
			tmpl = SubjectStoreVec
		}
		subject := svcHandler.SvcTopic(tmpl).MakeReqSubjectWithParams(map[string]any{"ACTION": action})
		evNode := inflowNodes.NewExtrinsicSvcNode(subject)
		evNode.ExtrinsicRule.ReqTimeoutSecound = 30
		payload := map[string]any{"action": action}
		// storeId/query/input/scope/key serve the doc store; text/topK serve the
		// vector store (the text to embed and how many neighbours a search returns).
		for _, k := range []string{"storeId", "query", "input", "scope", "key", "text", "topK"} {
			if v, ok := nodeData[k]; ok {
				payload[k] = v
			}
		}
		evNode.ExtrinsicRule.Data = payload
		node.Extrinsic = &evNode.ExtrinsicRule
	case NODE_UNTIL:
		// Continue After → an extrinsic on svc.continue.at. The schedule is either a
		// relative delay (a unit `mode` — seconds/minutes/hour/day — times `value`)
		// or an absolute `at` date/time. Both collapse to a single unix time: an
		// `at` is resolved to absolute epoch-millis (continueAt) here at compile
		// time; a delay is reduced to delaySeconds, which the continue.at handler
		// adds to the moment it is invoked at run time. The captured outbound nodes
		// are NOT resolved here — they travel in the compiled Node.Next, which the
		// handler reads from the request body to schedule the resumed run.
		node.Type = inflowModels.ExtrinsicNodeType
		evNode := inflowNodes.NewExtrinsicSvcNode(ContinueSubject, inflowNodes.WithUniqId[*inflowNodes.ExtrinsicSvcNode](vfn.ID))
		evNode.ExtrinsicRule.ReqTimeoutSecound = 10
		mode := getStr(nodeData, "mode")
		payload := map[string]any{"mode": mode}
		switch {
		case mode == "at":
			payload["at"] = getStr(nodeData, "at")
			payload["continueAt"] = parseAtMillis(getStr(nodeData, "at"))
		case delayModeSeconds(mode) > 0:
			payload["delaySeconds"] = int64(getFloat(nodeData, "value") * float64(delayModeSeconds(mode)))
		default:
			// Legacy nodes carried delaySeconds directly (pre unit/value modes).
			payload["delaySeconds"] = int64(getFloat(nodeData, "delaySeconds"))
		}
		evNode.ExtrinsicRule.Data = payload
		node.Extrinsic = &evNode.ExtrinsicRule
	case NODE_VOID:
		node.Type = inflowModels.VoidNodeType

	}

	return &node, nil
}
