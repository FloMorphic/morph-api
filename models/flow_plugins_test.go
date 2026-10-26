package models

import (
	"reflect"
	"testing"

	inflowModels "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// node is one graph node: its id, its type and the data the editor stamps on a
// plugin-backed node (pluginId / action).
func node(id, typ string, data map[string]any) inflowModels.VueFlowNode {
	n := inflowModels.VueFlowNode{ID: id, Type: typ}
	if data != nil {
		n.Data = data
	}
	return n
}

func edge(source, target string) inflowModels.Edges {
	return inflowModels.Edges{ID: source + "->" + target, Source: source, Target: target}
}

// TestCollectFlowPluginsConnectedOnly is the rule the whole field rests on: a
// plugin node nothing is wired to is not a plugin the flow needs. One edge —
// inbound or outbound — is enough to count, since a run can enter the graph
// mid-flow.
func TestCollectFlowPluginsConnectedOnly(t *testing.T) {
	graph := inflowModels.VueFlow{
		Nodes: []inflowModels.VueFlowNode{
			node("start", "startNode", nil),
			node("inbound", "llm", map[string]any{"pluginId": "aaaa-bbbb-cccc-llm0"}),
			node("outbound", "http", map[string]any{"pluginId": "aaaa-bbbb-cccc-http"}),
			node("tail", "js", nil),
			// Parked on the canvas with nothing connected: the save warns about
			// it (inflow's unreachable issue) and this list leaves it out.
			node("parked", "mcp", map[string]any{"pluginId": "aaaa-bbbb-cccc-mcp0"}),
		},
		Edges: []inflowModels.Edges{
			edge("start", "inbound"),
			edge("outbound", "tail"),
		},
	}

	got := CollectFlowPlugins(graph)
	want := []FlowPlugin{
		{Kind: KindBuiltin, Type: ExtHTTPType, PluginID: "aaaa-bbbb-cccc-http", NodeCount: 1},
		{Kind: KindBuiltin, Type: ExtLLMType, PluginID: "aaaa-bbbb-cccc-llm0", NodeCount: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plugins = %+v, want %+v", got, want)
	}
}

// TestCollectFlowPluginsGrouping covers the grouping: one entry per plugin, not
// per node — a builtin keyed by its type (so an unstamped legacy node joins the
// stamped one), an imported plugin by its id with its methods gathered, and the
// decision node's retired `jev` name folded into ai-decision.
func TestCollectFlowPluginsGrouping(t *testing.T) {
	graph := inflowModels.VueFlow{
		Nodes: []inflowModels.VueFlowNode{
			node("llm1", "llm", map[string]any{"pluginId": "aaaa-bbbb-cccc-llm0"}),
			// Hand-authored: same builtin, no stamped id.
			node("llm2", "llm", nil),
			node("jira1", "plugin", map[string]any{"pluginId": "jira-local", "action": "jira.add_task"}),
			node("jira2", "plugin", map[string]any{"pluginId": "jira-local", "action": "jira.update_task"}),
			node("jira3", "plugin", map[string]any{"pluginId": "jira-local", "action": "jira.add_task"}),
			node("decide", "ai-decision", map[string]any{"pluginId": "aaaa-bbbb-cccc-jev0"}),
			node("legacyDecide", "jev", nil),
		},
		Edges: []inflowModels.Edges{
			edge("llm1", "llm2"),
			edge("llm2", "jira1"),
			edge("jira1", "jira2"),
			edge("jira2", "jira3"),
			edge("jira3", "decide"),
			edge("decide", "legacyDecide"),
		},
	}

	got := CollectFlowPlugins(graph)
	want := []FlowPlugin{
		{Kind: KindBuiltin, Type: ExtDecisionType, PluginID: "aaaa-bbbb-cccc-jev0", NodeCount: 2},
		{Kind: KindBuiltin, Type: ExtLLMType, PluginID: "aaaa-bbbb-cccc-llm0", NodeCount: 2},
		{
			Kind: KindExtension, Type: ExtPluginBaseType, PluginID: "jira-local",
			Actions: []string{"jira.add_task", "jira.update_task"}, NodeCount: 3,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plugins = %+v, want %+v", got, want)
	}
	// Builtins sort first, which is also what a UI badges separately.
	if !got[0].IsBuiltin() || got[len(got)-1].IsBuiltin() {
		t.Fatalf("order = %+v, want the builtins first", got)
	}
}

// TestCollectFlowPluginsUnbound is the entry an operator most needs: an
// imported flow whose plugin is not installed here carries no pluginId, so the
// nodes are grouped by the action namespace and reported with an empty id —
// "this flow calls jira and nothing here serves it" — rather than dropped.
func TestCollectFlowPluginsUnbound(t *testing.T) {
	graph := inflowModels.VueFlow{
		Nodes: []inflowModels.VueFlowNode{
			node("a", "plugin", map[string]any{"action": "jira.add_task"}),
			node("b", "plugin", map[string]any{"action": "jira.update_task"}),
			// Nothing identifies this one: no id, no action, and it would not
			// compile either.
			node("c", "plugin", map[string]any{}),
			// Node data need not be an object at all on a graph nobody drew.
			node("d", "plugin", nil),
		},
		Edges: []inflowModels.Edges{edge("a", "b"), edge("b", "c"), edge("c", "d")},
	}

	got := CollectFlowPlugins(graph)
	want := []FlowPlugin{{
		Kind: KindExtension, Type: ExtPluginBaseType,
		Actions: []string{"jira.add_task", "jira.update_task"}, NodeCount: 2,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plugins = %+v, want %+v", got, want)
	}
}

// TestCollectFlowPluginsNone: a flow of builtin non-plugin nodes needs no
// plugin running, and says so with an absent list rather than an empty one.
func TestCollectFlowPluginsNone(t *testing.T) {
	graph := inflowModels.VueFlow{
		Nodes: []inflowModels.VueFlowNode{
			node("start", "startNode", nil),
			node("code", "js", nil),
			node("ask", "hitl", nil),
			node("docs", "docstore", nil),
		},
		Edges: []inflowModels.Edges{edge("start", "code"), edge("code", "ask"), edge("ask", "docs")},
	}
	if got := CollectFlowPlugins(graph); got != nil {
		t.Fatalf("plugins = %+v, want none", got)
	}
	if got := CollectFlowPlugins(inflowModels.VueFlow{}); got != nil {
		t.Fatalf("empty graph gave %+v, want none", got)
	}
}
