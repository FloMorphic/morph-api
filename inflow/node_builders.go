package inflow

// Per-node-type populate functions for the flow compiler. NodeBuilder
// (compiler.go) dispatches each vueFlow node type here; every buildXxx fills
// the already-scaffolded inflow node (id/title/key/scope are set) with its
// primitive-specific rule. The data-reading helpers they share live at the
// bottom of this file.

import (
	"fmt"
	"time"

	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	inflowNodes "github.com/Inflowenger/inflow-fusion/nodes"
	"github.com/Inflowenger/inflow-fusion/svcHandler"
)

// buildCodeNode lowers a JS / OPA node to a Code node; the `lang` field on the
// data selects the variant (js / opa).
func buildCodeNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
	node.Type = inflowModels.CodeNodeType

	if lang, ok := nodeData["lang"].(string); ok {
		if lang == string(inflowModels.JavaScriptLang) {
			newJsNode := inflowNodes.NewJsNode(nodeData["logic_rule"].(string))
			node.Code = &newJsNode.CodeRule
		} else if lang == string(inflowModels.OPALang) {
			newOpaNode := inflowNodes.NewOpaNode(
				nodeData["logic_rule"].(string),
				nodeData["opa_result"].(string),
				inflowNodes.WithCriteriaData(conditionsCriteria(nodeData)),
			)
			node.Code = &newOpaNode.CodeRule
		}
	}
	return nil
}

// buildRuleNode lowers a Rule / Contract node to a Contract; its handlers drive
// the routed outbound edges.
func buildRuleNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
	node.Type = inflowModels.RuleNodeType

	criteria := conditionsCriteria(nodeData)
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
	return nil
}

// buildGotoNode lowers a Goto node to a GoTo rule targeting another flow/node.
func buildGotoNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
	node.Type = inflowModels.GoToNodeType
	gotoNode := inflowNodes.NewGotoNode()
	if targetFlow, ok := nodeData["goto"].(map[string]any); ok {
		gotoNode.From(targetFlow["flowId"].(string), targetFlow["from_nodeId"].(string))
		gotoNode.To(targetFlow["flowId"].(string), targetFlow["end_nodeId"].(string))
	}
	node.GoTo = &gotoNode.GoToRule
	return nil
}

// buildLLMNode lowers an LLM node to a Plugin whose body is the llm plugin's
// exact RunBody contract — {settings, prompt, system_prompt, functions} — with
// `settings` taken from data.settings (the settings-profile values the frontend
// resolved onto the node).
func buildLLMNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
	node.Type = inflowModels.PluginNodeType
	pluginNode, err := newPluginNode(vfn, nodeData)
	if err != nil {
		return err
	}
	request := getStr(nodeData, "request")
	if request == "" {
		request = "run"
	}
	pluginNode.Request = request
	// The compiler doesn't interpret the prompt template — it ships the drawer's
	// `body` (messages, or a legacy prompt/system_prompt pair) straight through so
	// the plugin owns its own contract. Only settings and the bound functions,
	// which live outside `body`, are folded in.
	pluginNode.Body = map[string]any{
		"settings":  llmSettingsBody(getMap(nodeData, "settings")),
		"functions": boundFunctions(nodeData, true),
	}
	for k, v := range getMap(nodeData, "body") {
		pluginNode.Body[k] = v
	}
	node.Plugin = &pluginNode.PluginRule
	return nil
}

// buildMcpNode lowers an MCP node to a Plugin whose body is the mcp plugin's
// exact per-action contract:
//
//	call_tool → McpCallToolBody {connection, tool, arguments} (no LLM)
//	run       → McpRunBody {settings, connection, prompt, system_prompt,
//	            functions} — settings from the frontend-resolved data.settings;
//	            functions carry only names (live schemas are re-fetched).
//
// Frontend-only fields (mcpMode, function row id/title/inputSchema) are
// dropped here.
func buildMcpNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
	node.Type = inflowModels.PluginNodeType
	pluginNode, err := newPluginNode(vfn, nodeData)
	if err != nil {
		return err
	}
	request := getStr(nodeData, "request")
	if request == "" {
		// The frontend keeps `request` in lockstep with mcpMode; derive the
		// same mapping when a legacy node predates that.
		if getStr(nodeData, "mcpMode") == "llm" {
			request = "run"
		} else {
			request = "call_tool"
		}
	}
	pluginNode.Request = request
	conn := mcpConnection(nodeData)
	if request == "run" {
		// Settings, connection and bound functions live outside `body`; the prompt
		// template (body.messages) ships straight through untouched, same as the
		// LLM node — the plugin owns its own body contract.
		pluginNode.Body = map[string]any{
			"settings":   llmSettingsBody(getMap(nodeData, "settings")),
			"connection": conn,
			"functions":  boundFunctions(nodeData, false),
		}
		for k, v := range getMap(nodeData, "body") {
			pluginNode.Body[k] = v
		}
	} else {
		args, _ := nodeData["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		pluginNode.Body = map[string]any{
			"connection": conn,
			"tool":       getStr(nodeData, "tool"),
			"arguments":  args,
		}
	}
	node.Plugin = &pluginNode.PluginRule
	return nil
}

// buildCastNode lowers a Cast / Mapping node to a Plugin. Its config is the
// referenced store plus the mapping rows, carried in the body alongside any
// drawer-set body fields.
func buildCastNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
	node.Type = inflowModels.PluginNodeType
	pluginNode, err := newPluginNode(vfn, nodeData)
	if err != nil {
		return err
	}
	body, _ := nodeData["body"].(map[string]any)
	if body == nil {
		body = map[string]any{}
	}
	for _, k := range []string{"mappings", "storeId"} {
		if v, ok := nodeData[k]; ok {
			body[k] = v
		}
	}
	request := getStr(nodeData, "request")
	if request == "" {
		request = "run"
	}
	pluginNode.Body = body
	pluginNode.Request = request
	node.Plugin = &pluginNode.PluginRule
	return nil
}

// buildPluginActionNode lowers a node contributed by an imported plugin to a
// Plugin node calling one of that plugin's actions.
//
// It is the generic counterpart to the builtins above: those each know their
// plugin's body contract and shape it here, while this one knows nothing about
// the plugin it is calling. The action's own form defined the drawer's fields,
// so the collected values (`body`) ship through untouched — the plugin is the
// only authority on what it accepts. `settings` is folded in the same way as for
// the builtins: the frontend resolves the selected settings profile onto the
// node, and it is passed verbatim rather than projected onto a known contract.
//
// `request` is the action's method, stamped on the node when it was dropped from
// the palette. Without one there is no subject to call, so that is an error
// rather than a silent default.
func buildPluginActionNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
	node.Type = inflowModels.PluginNodeType
	pluginNode, err := newPluginNode(vfn, nodeData)
	if err != nil {
		return err
	}
	request := getStr(nodeData, "action")
	if request == "" {
		request = getStr(nodeData, "request")
	}
	if request == "" {
		return fmt.Errorf("plugin node %q has no action to call", vfn.ID)
	}
	pluginNode.Request = request

	body := getMap(nodeData, "body")
	if body == nil {
		body = map[string]any{}
	}
	if settings := getMap(nodeData, "settings"); len(settings) > 0 {
		body["settings"] = settings
	}
	pluginNode.Body = body
	node.Plugin = &pluginNode.PluginRule
	return nil
}

// buildHitlNode lowers a Human-in-the-Loop node to an extrinsic call to this
// backend's `hitl` svc. The subject carries the nodeId so the handler can
// recover it; the Data payload carries the title/questions to record on the
// task.
func buildHitlNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
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
	return nil
}

// buildStoreNode lowers a Doc / Vector store node to an extrinsic on
// svc.store.{doc,vec}.{ACTION}. The referenced store travels in the Data
// payload; the action selects the concrete subject. A `read` carries its
// `query` (SQL run against the store); a `write` carries its `input` (the
// JSONPath/scope of the value to store).
func buildStoreNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
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
	// storeId/query/input/key serve the doc store; text/topK serve the vector
	// store (the text to embed and how many neighbours a search returns);
	// partition is the vector record's namespace/tag an index stamps and a search
	// filters on. The node's scope is NOT carried: the runtime resolves it before
	// the call and delivers the scoped object as the request's `data`.
	for _, k := range []string{"storeId", "query", "input", "key", "text", "topK", "partition"} {
		if v, ok := nodeData[k]; ok {
			payload[k] = v
		}
	}
	evNode.ExtrinsicRule.Data = payload
	node.Extrinsic = &evNode.ExtrinsicRule
	return nil
}

// buildUntilNode lowers a Continue After node to an extrinsic on
// svc.continue.at. The schedule is either a relative delay (a unit `mode` —
// seconds/minutes/hour/day — times `value`) or an absolute `at` date/time. Both
// collapse to a single unix time: an `at` is resolved to absolute epoch-millis
// (continueAt) here at compile time; a delay is reduced to delaySeconds, which
// the continue.at handler adds to the moment it is invoked at run time. The
// captured outbound nodes are NOT resolved here — they travel in the compiled
// Node.Next, which the handler reads from the request body to schedule the
// resumed run.
func buildUntilNode(node *inflowModels.Node, vfn compiler.VueFlowNode, nodeData map[string]any) error {
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
	return nil
}

// ---- shared plugin-node scaffolding ----------------------------------------

// newPluginNode builds the Plugin-node scaffolding shared by every
// plugin-backed morphic node (LLM / MCP / Cast). The plugin's uniqId must equal
// the backing extension-table row; the front end stamps that id onto the node
// data as `extensionId` when the node is dropped from the palette (see
// pluginUniqId).
func newPluginNode(vfn compiler.VueFlowNode, data map[string]any) (*inflowNodes.PluginNode, error) {
	return inflowNodes.NewPluginNode(
		getStr(data, "title"),
		inflowNodes.WithUniqId[*inflowNodes.PluginNode](pluginUniqId(data, vfn.ID)),
		inflowNodes.WithIdleWaitMinutes(int8(15)),
	)
}

// pluginUniqId is the identity a plugin node is registered under. It must match
// the backing extension-table row (stamped onto the node data as `extensionId`
// by the front end when a node is dropped from the palette). Falls back to the
// node's own id so a standalone / unstamped node still gets a stable identity.
func pluginUniqId(data map[string]any, nodeID string) string {
	if id := getStr(data, "pluginId"); id != "" {
		return id
	}
	return nodeID
}

// llmSettingsBody projects the settings the frontend resolved onto the node
// (data.settings — the selected settings-profile's values, resolved on the
// front side; the compiler never touches the profile store) onto the exact
// LLMSettings contract the llm / mcp plugins read as `body.settings`. Extra
// keys are dropped so the compiled body carries only the contract fields.
func llmSettingsBody(profile map[string]any) map[string]any {
	return map[string]any{
		"provider":     getStr(profile, "provider"),
		"url":          getStr(profile, "url"),
		"model":        getStr(profile, "model"),
		"access_token": getStr(profile, "access_token"),
		"temperature":  getFloat(profile, "temperature"),
		"max_tokens":   int(getFloat(profile, "max_tokens")),
	}
}

// boundFunctions lowers the drawer's function rows to the BoundFunction wire
// shape ({name, description[, parameters]}), dropping the frontend-only fields
// (id, title). withParams keeps a row's JSON schema (`parameters`, or
// `inputSchema` on MCP-loaded tools); the MCP run body passes false because the
// plugin re-fetches live schemas from the server at run time — only the names
// select which tools are bound.
func boundFunctions(data map[string]any, withParams bool) []map[string]any {
	rows, _ := data["functions"].([]any)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name := getStr(row, "name")
		if name == "" {
			continue
		}
		fn := map[string]any{"name": name, "description": getStr(row, "description")}
		if withParams {
			if p, ok := row["parameters"].(map[string]any); ok {
				fn["parameters"] = p
			} else if p, ok := row["inputSchema"].(map[string]any); ok {
				fn["parameters"] = p
			}
		}
		out = append(out, fn)
	}
	return out
}

// mcpConnection builds the McpConnection contract from the node data — the MCP
// connection (url / transport / auth) lives on the node, not in a profile.
func mcpConnection(data map[string]any) map[string]any {
	return map[string]any{
		"url":       getStr(data, "url"),
		"transport": getStr(data, "transport"),
		"auth":      getStr(data, "auth"),
	}
}

// ---- data-reading helpers --------------------------------------------------

// conditionsCriteria flattens the drawer's `conditions` rows ([{key, value}])
// into the criteria map the code / contract rules consume.
func conditionsCriteria(data map[string]any) map[string]any {
	criteria := map[string]any{}
	if conds, ok := data["conditions"].([]any); ok {
		for _, el := range conds {
			if field, ok := el.(map[string]any); ok {
				criteria[field["key"].(string)] = field["value"]
			}
		}
	}
	return criteria
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

// getMap reads a sub-map field defensively (empty map when absent/other type).
func getMap(data map[string]any, key string) map[string]any {
	if m, ok := data[key].(map[string]any); ok {
		return m
	}
	return map[string]any{}
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
