package designer

// The AI ⇄ canvas contract on the Go side — the inverse of BuildPrompt.
//
// BuildPrompt tells a model to emit a *graph patch* (nodes named by a local
// `ref`, wired by designer-visible `port` names). PlanPatch turns that patch
// into a real Vue-Flow graph the compiler accepts: refs become ids, a `kind` is
// validated against the catalog, `data` is merged over the kind's defaults, the
// LLM/Rule route tags are derived, and each `port` is resolved to the
// `edge.data.tags` the inflow-fusion compiler actually routes on (it reads
// e.Data.Tags — sourceHandle is only carried as meta). This is the faithful port
// of flomorphic-wapp's aiGraph.ts planPatch, so an MCP-authored flow lands
// exactly as a human-drawn one, from the same guidance.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	"github.com/google/uuid"
)

// rootRefRe matches a fixed-root context template `{{ $. … }}` — the form that
// is wrong inside a many-scope node's text fields, where each pass is scoped to
// one element and per-row fields must be read with `{{$this…}}` instead.
var rootRefRe = regexp.MustCompile(`\{\{\s*\$\.`)

// exceptionTag is the port an LLM node with bound functions always grows, taken
// when the plugin errors or the model picks no function (mirrors aiGraph's
// EXCEPTION_TAG / exceptionPort).
const exceptionTag = "_exception"

// PatchNode is one node an assistant asks for — its `ref` is a local handle the
// patch's edges refer to, not the canvas id (assigned here).
type PatchNode struct {
	Ref      string         `json:"ref"`
	Kind     string         `json:"kind"`
	Title    string         `json:"title"`
	Key      string         `json:"key"`
	Scope    string         `json:"scope"`
	Position *Position      `json:"position,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Note     string         `json:"note,omitempty"`
}

// Position is an explicit node placement; when omitted the node is laid out by
// NormalizeGraph on save (same as any other headless-authored graph).
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PatchEdge is one connection. From/To are patch refs (or ids of nodes already
// on the canvas). Port names the source's output the designer's way — an LLM
// function name, a Rule handler tag, or `_exception` — required only when the
// source has derived ports.
type PatchEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Port string `json:"port,omitempty"`
	Note string `json:"note,omitempty"`
}

// Patch is the whole document a model emits under BuildPrompt.
type Patch struct {
	Nodes []PatchNode `json:"nodes"`
	Edges []PatchEdge `json:"edges,omitempty"`
	Notes []string    `json:"notes,omitempty"`
}

// Problem is one issue found while planning — `error` drops the offending
// node/edge, `warn` keeps it as-is (mirrors aiGraph's PatchProblem). These are
// the design mistakes the compiler alone cannot see (a many-scope on a routing
// node, two branches converging without a promissall).
type Problem struct {
	Level   string `json:"level"` // "error" | "warn"
	At      string `json:"at,omitempty"`
	Message string `json:"message"`
}

// derivedPort is one output port derived from a node's own data (an LLM
// function, a Rule handler). Its Tags are what an edge leaving it must carry.
type derivedPort struct {
	id   string
	tags []string
}

// kindByName indexes the catalog for kind validation and default lookup, built
// once alongside the catalog in the package init.
var kindByName map[string]nodeKind

func indexCatalog() {
	kindByName = make(map[string]nodeKind, len(catalog))
	for _, k := range catalog {
		kindByName[k.Kind] = k
	}
}

// PlanPatch converts a graph patch into a Vue-Flow graph plus the design
// problems found. `existing`, when given, lets an edge wire into a node already
// on the canvas (by id) — it is never mutated. The returned graph is not yet
// laid out; NormalizeGraph (the shared headless-author normalizer) places any
// node without a position on save.
func PlanPatch(patch Patch, existing *compiler.VueFlow) (compiler.VueFlow, []Problem) {
	var problems []Problem
	existingIDs := map[string]bool{}
	if existing != nil {
		for _, n := range existing.Nodes {
			existingIDs[n.ID] = true
		}
	}

	// ---- nodes ----
	nodes := make([]compiler.VueFlowNode, 0, len(patch.Nodes))
	idByRef := map[string]string{}
	dataByID := map[string]map[string]any{}
	typeByID := map[string]string{}

	for i, raw := range patch.Nodes {
		ref := strings.TrimSpace(raw.Ref)
		if ref == "" {
			ref = fmt.Sprintf("node%d", i+1)
		}
		if _, dup := idByRef[ref]; dup {
			problems = append(problems, Problem{Level: "error", At: ref,
				Message: fmt.Sprintf("Duplicate ref %q — only the first is added.", ref)})
			continue
		}
		if _, ok := kindByName[raw.Kind]; !ok && raw.Kind != "plugin" {
			problems = append(problems, Problem{Level: "error", At: ref,
				Message: fmt.Sprintf("Unknown node kind %q.", raw.Kind)})
			continue
		}

		data := mergeData(raw)
		id := "n-" + shortID()
		idByRef[ref] = id
		dataByID[id] = data
		typeByID[id] = raw.Kind

		node := compiler.VueFlowNode{ID: id, Type: raw.Kind, Data: data}
		if raw.Position != nil {
			node.Position = compiler.Position{X: raw.Position.X, Y: raw.Position.Y}
		}
		nodes = append(nodes, node)

		problems = append(problems, inspectScope(raw, data)...)
		problems = append(problems, inspectRowRefs(raw, data)...)
	}

	// ---- edges ----
	edges := make([]compiler.Edges, 0, len(patch.Edges))
	for _, raw := range patch.Edges {
		label := fmt.Sprintf("%s → %s", raw.From, raw.To)
		source := resolveEndpoint(raw.From, idByRef, existingIDs)
		target := resolveEndpoint(raw.To, idByRef, existingIDs)
		if source == "" || target == "" {
			missing := raw.From
			if source != "" {
				missing = raw.To
			}
			problems = append(problems, Problem{Level: "error", At: label,
				Message: fmt.Sprintf("Edge dropped — %q is not a node in this patch or on the canvas.", missing)})
			continue
		}

		handleID, tags, prob, drop := resolvePort(typeByID[source], dataByID[source], raw.Port)
		if prob != nil {
			prob.At = label
			problems = append(problems, *prob)
		}
		if drop {
			continue
		}
		edges = append(edges, compiler.Edges{
			ID:           "e-" + shortID(),
			Source:       source,
			Target:       target,
			SourceHandle: handleID,
			Data:         compiler.EdgePayload{Tags: tags},
		})
	}

	problems = append(problems, inspectConvergence(nodes, edges, existing)...)

	return compiler.VueFlow{Nodes: nodes, Edges: edges}, problems
}

// mergeData layers the patch node's data over the kind's catalog defaults, then
// hoists title/key/scope on top, and normalises the two shapes a model reliably
// gets wrong: the LLM/MCP message shorthand and the function/handler route tags.
// Mirrors aiGraph.mergeData.
func mergeData(raw PatchNode) map[string]any {
	data := defaultsFor(raw.Kind)
	for k, v := range raw.Data {
		data[k] = v
	}
	if s := strings.TrimSpace(raw.Title); s != "" {
		data["title"] = raw.Title
	}
	if raw.Key != "" {
		data["key"] = raw.Key
	}
	// scope defaults to "$" (the whole context) when the patch leaves it out —
	// "usually $", as the prompt puts it.
	if raw.Scope != "" {
		data["scope"] = raw.Scope
	} else if _, ok := data["scope"]; !ok {
		data["scope"] = "$"
	}

	// Prompt shorthand: models emit `system` / `prompt` reliably but botch the
	// nested body.messages array, so accept both and normalise to the one shape
	// the plugin reads.
	if raw.Kind == "llm" || raw.Kind == "mcp" {
		if msgs, ok := pickMessages(data); ok {
			body := asObject(data["body"])
			if body == nil {
				body = map[string]any{}
			}
			body["messages"] = msgs
			data["body"] = body
		}
		delete(data, "system")
		delete(data, "prompt")
		delete(data, "messages")
	}

	// Give function / handler rows a stable id so a later rename in the drawer
	// cannot orphan an edge, and mirror a handler's `name` into `tags` (the shape
	// the engine and compiler read) rather than trusting the model to write both.
	if raw.Kind == "llm" {
		data["functions"] = stampIDs(data["functions"], "fn")
	}
	if raw.Kind == "rule" {
		handlers := stampIDs(data["handlers"], "h")
		for _, h := range handlers {
			h["tags"] = handlerTags(h)
		}
		data["handlers"] = handlers
	}
	return data
}

// defaultsFor returns a fresh copy of a kind's catalog data defaults (never a
// shared map — each node gets its own). Unknown kinds (a `plugin` action) have
// no builtin defaults.
func defaultsFor(kind string) map[string]any {
	k, ok := kindByName[kind]
	if !ok || len(k.DataFields) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(k.DataFields, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// pickMessages builds the init messages implied by a node's data (the
// system/prompt shorthand plus any explicit messages), or (nil,false) to leave
// body untouched. Mirrors aiGraph.pickMessages: at most one system and one user
// row, system first.
func pickMessages(data map[string]any) ([]map[string]any, bool) {
	var rows []map[string]any
	hasRole := func(role string) bool {
		for _, r := range rows {
			if r["role"] == role {
				return true
			}
		}
		return false
	}

	if s, ok := data["system"].(string); ok && strings.TrimSpace(s) != "" {
		rows = append(rows, map[string]any{"role": "system", "content": s})
	}
	if u, ok := data["prompt"].(string); ok && strings.TrimSpace(u) != "" {
		rows = append(rows, map[string]any{"role": "user", "content": u})
	}

	// Explicit messages: data.messages, else body.messages.
	raw := asRows(data["messages"])
	fromBody := false
	if raw == nil {
		if body := asObject(data["body"]); body != nil {
			raw = asRows(body["messages"])
			fromBody = true
		}
	}
	for _, m := range raw {
		role := "user"
		if m["role"] == "system" {
			role = "system"
		}
		content, _ := m["content"].(string)
		if strings.TrimSpace(content) != "" && !hasRole(role) {
			rows = append(rows, map[string]any{"role": role, "content": content})
		}
	}

	if len(rows) == 0 {
		// No shorthand and no usable messages: only reset body.messages to empty
		// when there WAS a messages array to normalise; otherwise leave body be.
		if raw != nil && !fromBody {
			return []map[string]any{}, true
		}
		return nil, false
	}
	// system before user — the drawer edits exactly one of each in that order.
	if len(rows) == 2 && rows[0]["role"] == "user" && rows[1]["role"] == "system" {
		rows[0], rows[1] = rows[1], rows[0]
	}
	return rows, true
}

// stampIDs gives every row a stable `id`, keeping any the patch already set.
// Mirrors aiGraph.stampIds.
func stampIDs(value any, prefix string) []map[string]any {
	rows := asRows(value)
	out := make([]map[string]any, 0, len(rows))
	for i, row := range rows {
		cp := map[string]any{}
		for k, v := range row {
			cp[k] = v
		}
		if id, ok := cp["id"].(string); !ok || strings.TrimSpace(id) == "" {
			cp["id"] = fmt.Sprintf("%s-%s-%d", prefix, shortID(), i)
		}
		out = append(out, cp)
	}
	return out
}

// handlerTags is the route tags a Rule handler fires: its `name` (one tag), or
// its explicit `tags` for a legacy handler with no name. Mirrors
// nodeCatalog.handlerTags.
func handlerTags(h map[string]any) []string {
	if name, ok := h["name"].(string); ok && strings.TrimSpace(name) != "" {
		return []string{strings.TrimSpace(name)}
	}
	out := []string{}
	for _, t := range asSlice(h["tags"]) {
		if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// derivedPorts is the ports a node grows from its own data — only LLM functions
// and Rule handlers among the builtins (everything else has a single default
// handle). Mirrors the ports() of the llm / rule catalog specs.
func derivedPorts(nodeType string, data map[string]any) []derivedPort {
	switch nodeType {
	case "llm":
		fns := asRows(data["functions"])
		if len(fns) == 0 {
			return nil
		}
		ports := make([]derivedPort, 0, len(fns)+1)
		for i, f := range fns {
			id := firstStr(f["id"], f["name"], fmt.Sprintf("fn%d", i))
			name, _ := f["name"].(string)
			var tags []string
			if strings.TrimSpace(name) != "" {
				tags = []string{strings.TrimSpace(name)}
			}
			ports = append(ports, derivedPort{id: id, tags: tags})
		}
		ports = append(ports, derivedPort{id: exceptionTag, tags: []string{exceptionTag}})
		return ports
	case "rule":
		handlers := asRows(data["handlers"])
		ports := make([]derivedPort, 0, len(handlers))
		for i, h := range handlers {
			ports = append(ports, derivedPort{id: firstStr(h["id"], fmt.Sprintf("h%d", i)), tags: handlerTags(h)})
		}
		return ports
	default:
		return nil
	}
}

// resolvePort maps a designer-named port to the handle id and route tags an edge
// must carry. A node with no derived ports takes any edge on its default handle;
// a node with derived ports requires a matching port or the edge is dropped.
// Mirrors aiGraph.resolvePort, resolving against tag / id (the strings the
// prompt teaches).
func resolvePort(nodeType string, data map[string]any, requested string) (handleID string, tags []string, prob *Problem, drop bool) {
	ports := derivedPorts(nodeType, data)
	want := strings.TrimSpace(requested)

	if len(ports) == 0 {
		if want != "" {
			return "", nil, &Problem{Level: "warn",
				Message: fmt.Sprintf("Source has no named ports — %q ignored and the edge left on its default handle.", want)}, false
		}
		return "", nil, nil, false
	}
	if want == "" {
		return "", nil, &Problem{Level: "error",
			Message: fmt.Sprintf("Edge dropped — the source routes through named ports, so `port` is required. Available: %s.", portNames(ports))}, true
	}
	for _, p := range ports {
		if p.id == want || containsFold(p.tags, want) {
			return p.id, p.tags, nil, false
		}
	}
	return "", nil, &Problem{Level: "error",
		Message: fmt.Sprintf("Edge dropped — no port %q on the source. Available: %s.", want, portNames(ports))}, true
}

// inspectScope flags the mistake the compiler cannot see: a routing node (LLM
// with functions, Rule with handlers) given a many-valued scope. Such a node
// routes for the whole node, so the runtime stops at the first element that
// picks a branch and skips the rest — see the "many-scope limit" in the prompt.
func inspectScope(raw PatchNode, data map[string]any) []Problem {
	scope, _ := data["scope"].(string)
	if !isManyScope(scope) {
		return nil
	}
	routes := (raw.Kind == "llm" && len(asRows(data["functions"])) > 0) ||
		(raw.Kind == "rule" && len(asRows(data["handlers"])) > 0)
	if !routes {
		return nil
	}
	ref := firstStr(raw.Ref, raw.Title, raw.Kind)
	return []Problem{{Level: "warn", At: ref,
		Message: fmt.Sprintf("%q routes on its result but has a many-valued scope %q — the run stops at the first element that picks a branch and skips the rest. Give a routing node a single-valued scope (usually \"$\"); do the per-element work in a separate node and route once after.", ref, scope)}}
}

// inspectRowRefs flags the second half of the same mistake: a many-scope node
// whose text templates address the context by fixed root (`{{$.field}}`) and
// never use `{{$this…}}`. Each pass is scoped to ONE element, so a per-row field
// must be read as `{{$this.field}}`; `{{$.field}}` reads the whole-context root
// (usually absent on the pass) and `{{$.arr[0].field}}` reads the first row on
// every pass. The compiler cannot see this — it is a template mistake, so it is
// reported here, mirroring the "$this vs $.path" section of the prompt.
func inspectRowRefs(raw PatchNode, data map[string]any) []Problem {
	scope, _ := data["scope"].(string)
	if !isManyScope(scope) {
		return nil
	}
	var strs []string
	// `logic_rule` / `opa_result` are code, not text templates — `{{…}}` is
	// invalid there and handled separately — so they are not scanned.
	collectStrings(data, map[string]bool{"logic_rule": true, "opa_result": true}, &strs)
	joined := strings.Join(strs, "\n")
	if !rootRefRe.MatchString(joined) || strings.Contains(joined, "$this") {
		return nil
	}
	ref := firstStr(raw.Ref, raw.Title, raw.Kind)
	return []Problem{{Level: "warn", At: ref,
		Message: fmt.Sprintf("%q has a many-valued scope %q, but its text templates reference the context by fixed root (\"{{$.…}}\") and never use \"{{$this…}}\". Each pass is scoped to one element, so per-row fields must be read as \"{{$this.field}}\" — \"{{$.field}}\" reads the whole-context root (usually absent per pass), and \"{{$.arr[0].field}}\" reads the first row every pass.", ref, scope)}}
}

// collectStrings gathers every string value reachable in v, skipping the named
// keys (used to exclude code fields from a text-template scan).
func collectStrings(v any, skip map[string]bool, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case map[string]any:
		for k, val := range t {
			if skip[k] {
				continue
			}
			collectStrings(val, skip, out)
		}
	case []any:
		for _, e := range t {
			collectStrings(e, skip, out)
		}
	case []map[string]any:
		for _, e := range t {
			collectStrings(e, skip, out)
		}
	}
}

// inspectConvergence flags two branches arriving at one non-promissall node
// (inbound edges do not merge — the node runs once per edge), and a promissall
// fed by a node that itself runs more than once (the join then waits forever).
// Mirrors aiGraph.inspectConvergence, over this patch's edges plus the existing
// graph's.
func inspectConvergence(nodes []compiler.VueFlowNode, edges []compiler.Edges, existing *compiler.VueFlow) []Problem {
	var out []Problem

	type meta struct {
		label string
		kind  string
	}
	info := map[string]meta{}
	for _, n := range nodes {
		info[n.ID] = meta{label: nodeLabel(n), kind: n.Type}
	}
	if existing != nil {
		for _, n := range existing.Nodes {
			info[n.ID] = meta{label: nodeLabel(n), kind: n.Type}
		}
	}

	all := edges
	if existing != nil {
		all = append(append([]compiler.Edges{}, existing.Edges...), edges...)
	}
	// Convergence is about FORWARD fan-in — two branches racing into one node. A
	// loop's back-edge (the increment returning to the guard) also lands on a
	// node, but that is sequential RE-ENTRY, not a fork, so it must not read as
	// "runs twice". Exclude loop-closing edges from every inbound count here.
	back := loopClosingEdges(all)
	inbound := map[string]int{}
	for i, e := range all {
		if back[i] {
			continue
		}
		inbound[e.Target]++
	}
	// Only nodes this patch actually wires into are worth reporting.
	touched := map[string]bool{}
	for _, e := range edges {
		touched[e.Target] = true
	}
	multi := map[string]bool{}
	for id, n := range inbound {
		if n > 1 {
			multi[id] = true
		}
	}

	for id, count := range inbound {
		m, ok := info[id]
		if !ok || m.kind == "promissall" || !touched[id] || count <= 1 {
			continue
		}
		out = append(out, Problem{Level: "warn", At: m.label,
			Message: fmt.Sprintf("%d branches arrive at %q, so it runs %d times — inbound edges do not merge. If you meant \"when all are done\", put a Wait for All (promissall) in front of it and wire the branches into that.", count, m.label, count)})
	}

	// The reverse mistake: a Wait for All that has nothing to wait for. A
	// promissall only earns its place where two or more edges converge; fed by
	// one edge (e.g. placed after a single scoped node, whose passes are a queue
	// inside the node and never fork) it is a no-op that just adds a hop. Only
	// promissall nodes this patch introduced are reported.
	for _, n := range nodes {
		if n.Type != "promissall" {
			continue
		}
		if c := inbound[n.ID]; c < 2 {
			out = append(out, Problem{Level: "warn", At: nodeLabel(n),
				Message: fmt.Sprintf("%q is a Wait for All but only %d branch(es) reach it — it has nothing to join and is a no-op. A promissall is only needed where two or more independent edges converge and must all finish first. (Scope cardinality is not branching: a many-valued scope is a queue inside one node, whose single outbound edge fires once after every pass.)", nodeLabel(n), c)})
		}
	}
	for _, e := range edges {
		t, ok := info[e.Target]
		if !ok || t.kind != "promissall" || !multi[e.Source] {
			continue
		}
		s := info[e.Source]
		out = append(out, Problem{Level: "error", At: t.label,
			Message: fmt.Sprintf("%q waits on %q, which itself runs more than once. A Wait for All needs every branch it waits on to run the same number of times, or it waits forever and the run stalls until it times out.", t.label, s.label)})
	}
	return out
}

// loopClosingEdges marks each edge that closes a cycle — an edge u→v where v can
// already reach u through the graph, i.e. the back-edge of a loop. Used to keep
// convergence checks to genuine forward fan-in.
func loopClosingEdges(edges []compiler.Edges) []bool {
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
	}
	out := make([]bool, len(edges))
	for i, e := range edges {
		out[i] = reaches(adj, e.Target, e.Source)
	}
	return out
}

// reaches reports whether `to` is reachable from `from` following edges. A
// self-edge (from == to) counts as reachable, so a self-loop is loop-closing.
func reaches(adj map[string][]string, from, to string) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == to {
			return true
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, adj[n]...)
	}
	return false
}

/* --------------------------------------------------------------- small helpers */

func resolveEndpoint(ref string, idByRef map[string]string, existingIDs map[string]bool) string {
	key := strings.TrimSpace(ref)
	if key == "" {
		return ""
	}
	if id, ok := idByRef[key]; ok {
		return id
	}
	if existingIDs[key] {
		return key
	}
	return ""
}

func nodeLabel(n compiler.VueFlowNode) string {
	if data, ok := n.Data.(map[string]any); ok {
		if t, ok := data["title"].(string); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return n.ID
}

func portNames(ports []derivedPort) string {
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		if len(p.tags) > 0 {
			names = append(names, p.tags[0])
		} else {
			names = append(names, p.id)
		}
	}
	return strings.Join(names, ", ")
}

// isManyScope reports whether a JSONPath selects more than one value — a
// wildcard or a filter query. A plain dotted path selects one.
func isManyScope(scope string) bool {
	return strings.Contains(scope, "[*]") || strings.Contains(scope, "[?") || strings.Contains(scope, "..")
}

func asObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// asRows coerces an array-of-objects field to typed rows; nil when the field is
// absent (distinct from an empty array, which pickMessages treats differently).
func asRows(v any) []map[string]any {
	s, ok := v.([]any)
	if !ok {
		// Already-typed rows (from a prior pass in the same process).
		if rows, ok := v.([]map[string]any); ok {
			return rows
		}
		return nil
	}
	out := make([]map[string]any, 0, len(s))
	for _, e := range s {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func firstStr(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// shortID is a compact unique suffix for a generated node / edge id — the ids
// are regenerated on every import and mean nothing outside the graph.
func shortID() string {
	return strings.SplitN(uuid.NewString(), "-", 2)[0]
}
