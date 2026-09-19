package designer

import (
	"testing"
)

// A planned graph exported back to a patch must read like the patch that made
// it: refs from titles, hoisted key/scope, only non-default data, ports named the
// designer's way — and re-planning it must yield the same route tags.
func TestGraphToPatchRoundTrip(t *testing.T) {
	in := Patch{
		Nodes: []PatchNode{
			{Ref: "s", Kind: "startNode", Title: "Start"},
			{Ref: "fetch", Kind: "http", Title: "Fetch issues", Key: "issues", Data: map[string]any{
				"body": map[string]any{"method": "GET", "url": "https://x/y", "headers": []any{}, "query": []any{}, "body": "", "body_type": "json"},
			}},
			{Ref: "each", Kind: "js", Title: "Per issue", Scope: "$.issues[*]", Key: "seen", Data: map[string]any{"logic_rule": "input"}},
			{Ref: "decide", Kind: "rule", Title: "Decide", Data: map[string]any{
				"handlers": []any{map[string]any{"name": "approved"}, map[string]any{"name": "rejected"}},
			}},
			{Ref: "sink", Kind: "plugin", Title: "Store", Data: map[string]any{
				"action":      "db.stages.upsert",
				"pluginId":    "venapce-1234",
				"extensionId": "ext_x",
				"form":        map[string]any{"schema": map[string]any{}},
				"outbound":    []any{map[string]any{"title": "Stored", "tags": []any{"stored"}}, map[string]any{"title": "Failed", "tags": []any{"failed"}}},
				"body":        map[string]any{"stage": "audit"},
				"settingsId":  "",
				"settings":    map[string]any{"token": "secret"},
			}},
			{Ref: "end", Kind: "promissall", Title: "Join"},
		},
		Edges: []PatchEdge{
			{From: "s", To: "fetch"},
			{From: "fetch", To: "each"},
			{From: "each", To: "decide"},
			{From: "decide", To: "sink", Port: "approved"},
			{From: "sink", To: "end", Port: "Stored"},
			{From: "sink", To: "end", Port: "failed"},
		},
	}
	graph, problems := PlanPatch(in, nil)
	for _, p := range problems {
		if p.Level == "error" {
			t.Fatalf("plan error: %+v", p)
		}
	}
	if len(graph.Edges) != 6 {
		t.Fatalf("planned %d edges, want 6", len(graph.Edges))
	}

	out := GraphToPatch(graph)
	if len(out.Nodes) != 6 || len(out.Edges) != 6 {
		t.Fatalf("exported %d nodes / %d edges", len(out.Nodes), len(out.Edges))
	}
	byRef := map[string]PatchNode{}
	for _, n := range out.Nodes {
		byRef[n.Ref] = n
	}
	// Refs come from titles.
	if _, ok := byRef["fetch-issues"]; !ok {
		t.Fatalf("refs = %v, want one named fetch-issues", refs(out))
	}
	// A start node with nothing configured carries no data and no scope.
	if s := byRef["start"]; s.Data != nil || s.Scope != "" || s.Key != "" {
		t.Errorf("start node not minimal: %+v", s)
	}
	// Hoisted key / scope survive; "$" is left implicit.
	if e := byRef["per-issue"]; e.Scope != "$.issues[*]" || e.Key != "seen" {
		t.Errorf("per-issue hoist: %+v", e)
	}
	if f := byRef["fetch-issues"]; f.Scope != "" || f.Key != "issues" {
		t.Errorf("fetch hoist: %+v", f)
	}
	// http data equal to the catalog default (everything but url) is dropped at
	// the top level; body differs (url) so it is kept whole.
	if f := byRef["fetch-issues"]; f.Data["subject_prefix"] != nil || f.Data["body"] == nil {
		t.Errorf("fetch data not minimised: %v", f.Data)
	}
	// The plugin node keeps its action and body, loses identity + secrets.
	sink := byRef["store"]
	if sink.Data["action"] != "db.stages.upsert" || sink.Data["body"] == nil {
		t.Errorf("plugin node lost portable data: %v", sink.Data)
	}
	for _, k := range []string{"pluginId", "extensionId", "form", "outbound", "settings", "settingsId"} {
		if _, ok := sink.Data[k]; ok {
			t.Errorf("plugin node exported install-local %q: %v", k, sink.Data)
		}
	}
	// Ports are named the designer's way.
	ports := map[string]string{}
	for _, e := range out.Edges {
		ports[e.From+"→"+e.To+"#"+e.Port] = e.Port
	}
	for _, want := range []string{"decide→store#approved", "store→join#stored", "store→join#failed", "start→fetch-issues#"} {
		if _, ok := ports[want]; !ok {
			t.Errorf("missing edge %q in %v", want, ports)
		}
	}

	// Re-plan: the rule edge routes on the handler tag again. (The plugin ports
	// need the outbound re-stamped first — that is the importer's job.)
	again, _ := PlanPatch(out, nil)
	tags := map[string][]string{}
	for _, e := range again.Edges {
		tags[e.Source+"→"+e.Target] = e.Data.Tags
	}
	found := false
	for _, e := range again.Edges {
		if len(e.Data.Tags) == 1 && e.Data.Tags[0] == "approved" {
			found = true
		}
	}
	if !found {
		t.Errorf("re-planned rule edge lost its tag: %v", tags)
	}
}

func TestPluginOutboundPorts(t *testing.T) {
	data := map[string]any{"outbound": []any{
		map[string]any{"title": "Stored", "tags": []any{"stored", "ok"}},
		map[string]any{"tags": []any{"failed"}},
		map[string]any{},
	}}
	ports := derivedPorts("plugin", data)
	if len(ports) != 3 {
		t.Fatalf("got %d ports", len(ports))
	}
	if ports[0].id != "Stored" || ports[0].tags[0] != "stored" {
		t.Errorf("port 0 = %+v", ports[0])
	}
	if ports[1].id != "failed" {
		t.Errorf("port 1 = %+v", ports[1])
	}
	if ports[2].id != "out2" {
		t.Errorf("port 2 = %+v", ports[2])
	}
	// resolvePort accepts the title or any tag; a bare edge is refused.
	if id, tags, _, drop := resolvePort("plugin", data, "ok"); drop || id != "Stored" || len(tags) != 2 {
		t.Errorf("resolve by tag: id=%q tags=%v drop=%v", id, tags, drop)
	}
	if _, _, prob, drop := resolvePort("plugin", data, ""); !drop || prob == nil {
		t.Errorf("a plugin with outbound must require a port")
	}
	if _, _, _, drop := resolvePort("plugin", map[string]any{}, ""); drop {
		t.Errorf("a plugin without outbound keeps its default handle")
	}
}

func TestUniqueRef(t *testing.T) {
	taken := map[string]bool{}
	if r := uniqueRef("Fleet: TCP listeners + owning process — long tail here", taken); r != "fleet-tcp-listeners-owning-proce" {
		t.Errorf("ref = %q", r)
	}
	if r := uniqueRef("Classify", taken); r != "classify" {
		t.Errorf("ref = %q", r)
	}
	if r := uniqueRef("Classify", taken); r != "classify-2" {
		t.Errorf("dup ref = %q", r)
	}
	if r := uniqueRef("—", taken); r != "node" {
		t.Errorf("empty ref = %q", r)
	}
}

func refs(p Patch) []string {
	out := make([]string, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		out = append(out, n.Ref)
	}
	return out
}
