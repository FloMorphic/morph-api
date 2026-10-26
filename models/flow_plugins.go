package models

import (
	"sort"
	"strings"

	inflowModels "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// The plugin bill of materials of a saved flow: which plugins a flow actually
// runs through, derived from its graph on every save (see CollectFlowPlugins).
//
// A plugin-backed node is the one kind of node whose work happens OUTSIDE this
// backend: a process the operator has to be running for the flow to work. That
// makes "which plugins does this flow need" a question asked away from the
// canvas — by the flow list, by whatever installed the flow, by an operator
// about to launch one — and answering it meant reading the graph, which is the
// whole weight of a FlowRecord and the one thing a list does not load (see
// FlowSummary). So the answer is stored beside the flow instead, as the handful
// of entries below.

// FlowPlugin is one plugin a flow contributes work to, and how.
//
// `Kind` is the tag the UI badges a builtin with: KindBuiltin for the plugins
// that ship with FloMorphic and are run from the builtin-plugins repo (the LLM,
// MCP, Cast, HTTP and AI-Decision nodes), KindExtension for a plugin the user
// imported through the Extension portal. Both are plugins — the same inflowv1
// protocol, the same "is it running?" question — and the difference is only who
// owns them, which is exactly what a reader of the list wants to know.
//
// There is deliberately no display name here. The palette row is the authority
// on what a plugin is called (and what icon it draws), a UI showing this list
// has that catalog loaded anyway, and a name copied in at save time would be
// the one field of the entry that goes stale when a plugin is renamed. What
// identifies the plugin is `Type` for a builtin (one builtin kind is one
// plugin) and `PluginID` for an imported one.
type FlowPlugin struct {
	// Kind separates the builtins from the imported plugins. See above.
	Kind ExtensionKind `json:"kind"`
	// Type is the node type the graph carries: the specific builtin kind
	// (llm, mcp, cast, http, ai-decision) for a builtin, and the generic
	// `plugin` for an imported plugin's action node. A node saved under the
	// retired `jev` type is reported as ai-decision — same plugin, former name
	// (see ExtJevType).
	Type ExtensionType `json:"type"`
	// PluginID is the inflowv1 PLUGIN_ID the nodes are bound to. It is a
	// per-install address, not a portable identity, so it travels in the
	// database and never in an exported document.
	//
	// Empty means the nodes are NOT bound to a plugin on this install: an
	// imported flow whose plugin is not installed here, or one whose action
	// several local plugins serve and whose binding is still an open question
	// (see flowfile's stampIdentities). Such an entry is the most useful one in
	// the list — it is what the operator has to install or pick — so it is
	// reported rather than dropped, identified by its `Actions`.
	PluginID string `json:"pluginId,omitempty"`
	// Actions are the plugin's action methods the flow calls, sorted and
	// de-duplicated. They are the portable half of a plugin's identity (an
	// `@actions` method name is the same on every install of the plugin), which
	// is what makes them the label for an entry that has no PluginID yet.
	// Empty for a builtin, whose single action is implied by its type.
	Actions []string `json:"actions,omitempty"`
	// NodeCount is how many nodes of the flow this plugin serves.
	NodeCount int `json:"nodeCount"`
}

// IsBuiltin reports whether this is one of the plugins that ship with
// FloMorphic rather than one the user imported.
func (p FlowPlugin) IsBuiltin() bool { return p.Kind == KindBuiltin }

// PluginBackedNodeType reports whether a graph node type is served by a plugin
// process, and under which identity: the canonical node type and whether that
// plugin is a builtin or an imported extension.
//
// It is the list of node kinds that lower to an inflow Plugin node (see the
// compiler's NodeBuilder). The rest of the palette — a JS node, a rule, a HITL,
// a store node — is run by this backend or by the engine, so none of it belongs
// in a flow's plugin list even though some of those nodes do reach outside.
func PluginBackedNodeType(nodeType string) (ExtensionType, ExtensionKind, bool) {
	switch t := ExtensionType(strings.TrimSpace(nodeType)); t {
	case ExtPluginBaseType:
		return ExtPluginBaseType, KindExtension, true
	case ExtLLMType, ExtMCPType, ExtCastType, ExtHTTPType, ExtDecisionType:
		return t, KindBuiltin, true
	case ExtJevType:
		// The decision node's former name. One plugin, so one entry: a flow
		// mixing nodes saved under both names must not list it twice.
		return ExtDecisionType, KindBuiltin, true
	default:
		return "", "", false
	}
}

// CollectFlowPlugins derives a flow's plugin list from its graph: one entry per
// plugin behind the plugin-backed nodes that are WIRED INTO the flow.
//
// Only a node with at least one edge on it — inbound or outbound — is counted.
// A plugin node sitting on the canvas with nothing connected to it is a node
// an author is still placing: it cannot run, nothing about the flow depends on
// the plugin behind it, and telling an operator to install a plugin for it
// would be wrong. The graph validation already says so where it belongs, as a
// warning on save with the node named (see inflow's unreachableIssues); this
// list simply leaves it out. Reachability from the start node is NOT the test
// here: a run can enter the graph mid-flow (a HITL resume, a Continue After, a
// partial run), so a connected node is a node that can run.
//
// The result is derived, never authored: it is recomputed from the graph on
// every save, so a `plugins` field a client sends is ignored, and the stored
// list can never drift from the graph it describes. It is sorted — builtins
// first, then by type, plugin id and actions — so two saves of the same graph
// store the same list and a client diffing it sees no churn.
func CollectFlowPlugins(graph inflowModels.VueFlow) []FlowPlugin {
	connected := make(map[string]bool, len(graph.Edges)*2)
	for _, e := range graph.Edges {
		if e.Source != "" {
			connected[e.Source] = true
		}
		if e.Target != "" {
			connected[e.Target] = true
		}
	}

	type group struct {
		entry   FlowPlugin
		actions map[string]bool
	}
	groups := map[string]*group{}
	for _, n := range graph.Nodes {
		typ, kind, ok := PluginBackedNodeType(n.Type)
		if !ok || !connected[n.ID] {
			continue
		}
		data, _ := n.Data.(map[string]any)
		pluginID := strings.TrimSpace(nodeDataString(data, "pluginId"))
		action := strings.TrimSpace(nodeDataString(data, "action"))

		// The grouping key is the plugin, not the node. A builtin is keyed by
		// its type: one builtin kind is one plugin on an install, which also
		// keeps a legacy node that carries no stamped pluginId in the same
		// entry as a freshly dropped one. An imported plugin is keyed by its
		// id, falling back to the action's namespace while nothing binds it —
		// `jira.add_task` and `jira.update_task` on an uninstalled plugin are
		// one plugin to install, not two.
		key := "builtin:" + string(typ)
		if kind == KindExtension {
			switch {
			case pluginID != "":
				key = "plugin:" + pluginID
			case action != "":
				key = "action:" + pluginNamespace(action)
			default:
				// Neither bound nor named: nothing here says which plugin the
				// node means, and the node cannot compile either.
				continue
			}
		}

		g := groups[key]
		if g == nil {
			g = &group{
				entry:   FlowPlugin{Kind: kind, Type: typ, PluginID: pluginID},
				actions: map[string]bool{},
			}
			groups[key] = g
		}
		g.entry.NodeCount++
		// A builtin's action is its type (`run`, `call_tool`, … are the node's
		// own request field, not a plugin the operator picks), so only an
		// imported plugin's methods are listed.
		if kind == KindExtension && action != "" {
			g.actions[action] = true
		}
		// One stamped node is enough to bind the group: a graph can hold a node
		// that was never stamped (hand-authored, or imported while the plugin
		// was missing) next to one that was.
		if g.entry.PluginID == "" && pluginID != "" {
			g.entry.PluginID = pluginID
		}
	}
	if len(groups) == 0 {
		return nil
	}

	out := make([]FlowPlugin, 0, len(groups))
	for _, g := range groups {
		if len(g.actions) > 0 {
			g.entry.Actions = make([]string, 0, len(g.actions))
			for a := range g.actions {
				g.entry.Actions = append(g.entry.Actions, a)
			}
			sort.Strings(g.entry.Actions)
		}
		out = append(out, g.entry)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.IsBuiltin() != b.IsBuiltin() {
			return a.IsBuiltin()
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.PluginID != b.PluginID {
			return a.PluginID < b.PluginID
		}
		return strings.Join(a.Actions, ",") < strings.Join(b.Actions, ",")
	})
	return out
}

// pluginNamespace is the plugin half of an action method (`jira` of
// `jira.add_task`) — what identifies an imported plugin's entry while no local
// plugin binds it. Mirrors flowfile's namespaceOf.
func pluginNamespace(action string) string {
	if i := strings.IndexByte(action, '.'); i > 0 {
		return action[:i]
	}
	return action
}

// nodeDataString reads a string field off a node's `data` object, which is
// `any` on the graph and need not be an object at all (a hand-authored or
// imported graph is not always one the editor drew).
func nodeDataString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	s, _ := data[key].(string)
	return s
}
