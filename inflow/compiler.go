package inflow

import (
	"encoding/json"
	"fmt"

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
	NODE_PLUGIN   = "pluginNative"
	NODE_START    = "startNode"
	NODE_VOID     = "void"
	NODE_CONTRACT = "contract"
	NODE_CODE     = "code"
	NODE_EXT_SVC  = "extrinsic"
	NODE_GOTO     = "goto"

	// Extensions
	NODE_MY_A = "my_a_ext"
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
	compiledNodes := []inflowModels.Node{}
	for _, el := range l {
		compiledNodes = append(compiledNodes, *el)
	}

	return startNodeId, l, nil
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
	case NODE_START:
		node.Type = inflowModels.VoidNodeType
	case NODE_MY_A:
		node.Type = inflowModels.ExtrinsicNodeType
		ext := models.ExtensionRecord{}
		json.Unmarshal([]byte(nodeData["extension_raw"].(string)), &ext)
		values := map[string]any{}
		for k, v := range ext.BindTo.Values {
			values[k] = v
		}
		svc := svcHandler.GetSvc(ext.BindTo.TopicKey)
		if svc == "" {
			return nil, fmt.Errorf("invalid data as %s", NODE_MY_A)
		}
		evNode := inflowNodes.NewExtrinsicSvcNode(
			svcHandler.SvcTopic(svc).MakeReqSubjectWithParams(values),
		)
		evNode.ExtrinsicRule.ReqTimeoutSecound = 5
		node.Extrinsic = &evNode.ExtrinsicRule
	case NODE_CODE:
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

	case NODE_CONTRACT:
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

	case NODE_EXT_SVC:
		node.Type = inflowModels.ExtrinsicNodeType
		if subject, ok := nodeData["serviceTopic"].(string); ok {
			evNode := inflowNodes.NewExtrinsicSvcNode(subject)
			node.Extrinsic = &evNode.ExtrinsicRule
		}
	case NODE_GOTO:
		node.Type = inflowModels.GoToNodeType
		gotoNode := inflowNodes.NewGotoNode()
		if targetFlow, ok := nodeData["goto"].(map[string]any); ok {
			gotoNode.From(targetFlow["flowId"].(string), targetFlow["from_nodeId"].(string))
			gotoNode.To(targetFlow["flowId"].(string), targetFlow["end_nodeId"].(string))

		}
		node.GoTo = &gotoNode.GoToRule
	case NODE_PLUGIN:
		node.Type = inflowModels.PluginNodeType
		// pluginUniqId:=fmt.Sprintf("%s-%s",nodeData["title"],vfn.ID)
		pluginNode, err := inflowNodes.NewPluginNode(
			nodeData["title"].(string),
			// inflowNodes.WithUniqId[*inflowNodes.PluginNode](pluginUniqId),
			inflowNodes.WithCustomPrefix(nodeData["subject_prefix"].(string)),
			inflowNodes.WithIdleWaitMinutes(int8(nodeData["idle_min"].(float64))),
		)
		pluginNode.Body = nodeData["body"].(map[string]any)
		pluginNode.Request = nodeData["request"].(string)
		if err != nil {
			return nil, err
		}

		node.Plugin = &pluginNode.PluginRule
	case NODE_VOID:
		node.Type = inflowModels.VoidNodeType

	}

	return &node, nil
}
