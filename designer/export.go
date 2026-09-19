package designer

// The inverse of PlanPatch: a saved Vue-Flow graph back to the graph patch — the
// portable, model-facing form of a workflow. Faithful port of flomorphic-wapp's
// aiGraph.ts graphToPatch, so a flow exported here reads the same as one the
// editor's "Export" button writes, and re-imports through PlanPatch.

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// installLocalKeys is the node data that never leaves an install: `title`,
// `key` and `scope` are hoisted onto the patch node; `extensionId` / `pluginId`
// / `form` / `outbound` are one install's extension identity, re-stamped from
// the target's own table on import (a pluginId is a per-install address, and a
// plugin node compiles its NATS subject straight from it); `settings` is the
// resolved settings profile — the provider token — which must never ride along
// in a file meant to be shared, and `settingsName` its denormalized label. A
// profile leaves by reference only (`settingsId`).
var installLocalKeys = map[string]bool{
	"title": true, "key": true, "scope": true,
	"extensionId": true, "pluginId": true, "form": true, "outbound": true,
	"settings": true, "settingsName": true,
	// The import-time "no local plugin provides this action" marker is a
	// property of the install that read the file, not of the flow.
	"missingPlugin": true,
}

var refSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// GraphToPatch turns a Vue-Flow graph into a graph patch: canvas ids become
// readable refs derived from each node's title, handle ids become designer port
// names, and each node carries only the data that differs from its kind's
// catalog defaults (so a reader sees what the designer actually configured).
// Ids and handle ids are dropped on purpose — they are regenerated on import and
// mean nothing outside the graph that made them. See installLocalKeys for what
// else is stripped and why.
func GraphToPatch(graph compiler.VueFlow) Patch {
	refByID := map[string]string{}
	taken := map[string]bool{}
	nodeByID := map[string]compiler.VueFlowNode{}

	nodes := make([]PatchNode, 0, len(graph.Nodes))
	for i, n := range graph.Nodes {
		nodeByID[n.ID] = n
		data := asObject(n.Data)
		defaults := defaultsFor(n.Type)
		title := strings.TrimSpace(asStr(data["title"]))

		ref := uniqueRef(firstStr(title, kindLabel(n.Type), n.Type, fmt.Sprintf("node%d", i+1)), taken)
		refByID[n.ID] = ref

		out := PatchNode{Ref: ref, Kind: n.Type, Title: title}
		// key / scope are hoisted out of data like the patch shape wants, but
		// only when they say something the catalog default does not.
		if k := asStr(data["key"]); k != "" && !sameJSON(data["key"], defaults["key"]) {
			out.Key = k
		}
		// "$" is the implicit scope every kind starts with (mergeData fills it in),
		// so only another value is worth writing.
		if sc := asStr(data["scope"]); sc != "" && sc != "$" && !sameJSON(data["scope"], defaults["scope"]) {
			out.Scope = sc
		}
		if n.Position.X != 0 || n.Position.Y != 0 {
			out.Position = &Position{X: math.Round(n.Position.X), Y: math.Round(n.Position.Y)}
		}
		if custom := changedData(data, defaults); len(custom) > 0 {
			out.Data = custom
		}
		nodes = append(nodes, out)
	}

	edges := make([]PatchEdge, 0, len(graph.Edges))
	for _, e := range graph.Edges {
		from, to := refByID[e.Source], refByID[e.Target]
		// An edge whose endpoints are not both in this graph has nothing to name
		// it by; it cannot exist in a saved flow, so there is nothing to report.
		if from == "" || to == "" {
			continue
		}
		edge := PatchEdge{From: from, To: to}
		if src, ok := nodeByID[e.Source]; ok {
			edge.Port = portName(src, e.SourceHandle, e.Data.Tags)
		}
		edges = append(edges, edge)
	}
	return Patch{Nodes: nodes, Edges: edges}
}

// changedData is node data minus the hoisted / install-local keys and anything
// left at its catalog default. A plugin node keeps its `action` — the method
// name the plugin declares, the same on every install, which is what the target
// resolves the node against.
func changedData(data, defaults map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range data {
		if installLocalKeys[k] || v == nil {
			continue
		}
		// A carried-but-empty profile id (a cleared selection) is not worth exporting.
		if k == "settingsId" && strings.TrimSpace(asStr(v)) == "" {
			continue
		}
		if dv, ok := defaults[k]; ok && sameJSON(v, dv) {
			continue
		}
		out[k] = v
	}
	return out
}

// portName is what a designer calls the port an edge leaves through — the name
// resolvePort reads back: the port's first tag, else its id. Matched by handle
// id first, then by the edge's own tags (a graph whose handle ids were lost to a
// re-plan still names its ports), and empty for a node with no derived ports.
func portName(src compiler.VueFlowNode, handleID string, tags []string) string {
	ports := derivedPorts(src.Type, asObject(src.Data))
	if len(ports) == 0 {
		return ""
	}
	pick := func(p derivedPort) string {
		if len(p.tags) > 0 {
			return p.tags[0]
		}
		return p.id
	}
	if handleID != "" {
		for _, p := range ports {
			if p.id == handleID {
				return pick(p)
			}
		}
	}
	for _, t := range tags {
		for _, p := range ports {
			if containsFold(p.tags, t) {
				return pick(p)
			}
		}
	}
	return ""
}

// uniqueRef makes a node's title a short, unique, readable patch ref
// (`classify`, `classify-2`). Mirrors aiGraph.uniqueRef.
func uniqueRef(source string, taken map[string]bool) string {
	base := refSlugRe.ReplaceAllString(strings.ToLower(source), "-")
	base = strings.Trim(base, "-")
	if len(base) > 32 {
		base = strings.TrimRight(base[:32], "-")
	}
	if base == "" {
		base = "node"
	}
	ref := base
	for i := 2; taken[ref]; i++ {
		ref = fmt.Sprintf("%s-%d", base, i)
	}
	taken[ref] = true
	return ref
}

// kindLabel is the catalog's display label for a kind ("Wait for All"), used
// to name an untitled node's ref; empty for a kind outside the catalog.
func kindLabel(kind string) string {
	if k, ok := kindByName[kind]; ok {
		return k.Label
	}
	return ""
}

// sameJSON compares two values by their JSON encoding, the way the wapp does
// (a data value read back from the store is generic, so type identity is moot).
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

func asStr(v any) string {
	s, _ := v.(string)
	return s
}
