// Package flowfile is the portable workflow document — the file the editor's
// Export button writes, the flow-cookbook ships, and an assistant can read or
// author — and the one road it takes onto (Import) and off (Export) an install.
//
// The document IS a designer graph patch (designer.Patch: nodes named by a
// local `ref`, wired by designer-visible `port` names, carrying only the data a
// node actually changed) under a small header. That is what makes it the right
// shape for anything that is not the canvas: it is about half the size of the
// saved FlowRecord (which carries Vue-Flow render state — computedPosition,
// dimensions, handleBounds, an embedded sourceNode/targetNode on every edge),
// it is readable and diffable, and it holds no install-local identity — no
// canvas ids, no extension row ids, no pluginId (a per-install address), no
// resolved settings profile (the provider token). See lib/exportFlow.ts in the
// wapp for the format's rationale; this is its Go twin.
//
// Both the REST /flow/import + /flow/id/:id/export routes and the MCP
// flo_import_workflow / flo_export_workflow / flo_apply_patch tools go through
// this package, so a flow that lands from any of them is identical to one drawn
// by hand: planned by designer.PlanPatch, its plugin nodes re-stamped with THIS
// install's extension identity, laid out by inflow.NormalizeGraph, saved by the
// same repository upsert.
package flowfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/designer"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
)

// Version is bumped only if the header's shape changes — the patch itself
// versions with the node catalog. Mirrors the wapp's WORKFLOW_FILE_VERSION.
const Version = 1

// Header is the `flomorphic` envelope field that marks a document as a
// workflow export. Nothing reads exportedAt back; it is for the reader.
type Header struct {
	Kind       string `json:"kind"`
	Version    int    `json:"version"`
	ExportedAt string `json:"exportedAt,omitempty"`
}

// PluginManifestEntry is one imported plugin a workflow depends on, recorded so
// a target install can tell the operator what to install before the flow runs.
// The match key is `actions`, never a plugin id: an install's pluginId is a
// per-install address, while the action method names a plugin declares in its
// `@actions` are the same on every install of it.
type PluginManifestEntry struct {
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
	Repo    string   `json:"repo,omitempty"`
	Ref     string   `json:"ref,omitempty"`
	Subdir  string   `json:"subdir,omitempty"`
}

// Document is the export file: header + title + plugin manifest + the patch.
// Parse accepts any object with a `nodes` array, header or not, so a bare patch
// a model wrote is as importable as a file the editor exported.
type Document struct {
	Flomorphic *Header               `json:"flomorphic,omitempty"`
	Title      string                `json:"title,omitempty"`
	Plugins    []PluginManifestEntry `json:"plugins,omitempty"`
	Nodes      []designer.PatchNode  `json:"nodes"`
	Edges      []designer.PatchEdge  `json:"edges,omitempty"`
	Notes      []string              `json:"notes,omitempty"`
}

// Patch is the document's graph patch, ready for the designer planner.
func (d Document) Patch() designer.Patch {
	return designer.Patch{Nodes: d.Nodes, Edges: d.Edges, Notes: d.Notes}
}

// Parse reads a document from JSON. The only hard requirement is a non-empty
// `nodes` array; the header is optional. The error text is meant to be shown.
func Parse(raw []byte) (Document, error) {
	var doc Document
	if len(raw) == 0 {
		return doc, errors.New("the workflow document is empty")
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("not a workflow export: %w", err)
	}
	if len(doc.Nodes) == 0 {
		return doc, errors.New(`the document holds no workflow — expected a JSON object with a "nodes" array`)
	}
	return doc, nil
}

// Input is one import: the document plus the caller's choices.
type Input struct {
	Doc Document
	// Workflow id to overwrite (a re-install); empty creates a new flow.
	ID string
	// Overrides the document's own title when set.
	Title string
	// Plan, stamp and compile-check without saving.
	DryRun bool
}

// MissingAction is one plugin action the document calls that no local plugin
// provides, with what the document knows about where to get it and the refs of
// the nodes calling it.
type MissingAction struct {
	Action string   `json:"action"`
	Plugin string   `json:"plugin"`
	Repo   string   `json:"repo,omitempty"`
	Ref    string   `json:"ref,omitempty"`
	Subdir string   `json:"subdir,omitempty"`
	Nodes  []string `json:"nodes"`
}

// Result is what an import produced. Flow is the record as saved (or as it
// would be saved on a dry run). Problems are the designer's planning findings;
// MissingActions the plugin actions this install cannot serve. The compile pass
// is a check, not a gate — see Import — so CompileError is reported, and on
// success StartNodeID / Compiled carry the lowered engine graph.
type Result struct {
	Flow           models.FlowRecord             `json:"flow"`
	Problems       []designer.Problem            `json:"problems"`
	MissingActions []MissingAction               `json:"missingActions"`
	CompileError   string                        `json:"compileError,omitempty"`
	StartNodeID    string                        `json:"startNodeId,omitempty"`
	Compiled       map[string]*inflowModels.Node `json:"compiled,omitempty"`
	DryRun         bool                          `json:"dryRun"`
}

// ErrInvalid marks an import rejected for its content (as opposed to a store
// failure); callers map it to a 400 / tool error.
var ErrInvalid = errors.New("invalid workflow")

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

// Import lands a document on this install the way the editor's Import dialog
// does: every plugin-backed node is stamped with this install's extension
// identity (before planning, so a plugin action's outbound ports resolve the
// `port` names on its edges), the patch is planned into a Vue-Flow graph, laid
// out, compile-checked and — unless DryRun — saved.
//
// Compiling is a check only, the way the editor's Run does later: plugin nodes
// resolve their account through the live inflow backend, so it cannot be a gate
// (an install being provisioned has none yet), and the Import dialog / apply-
// patch save without it too. A plugin node whose action no local plugin
// provides is kept, flagged with the same `missingPlugin` marker the canvas
// badges, and reported in MissingActions.
func Import(ctx context.Context, store repository.Store, in Input) (*Result, error) {
	doc := in.Doc
	if len(doc.Nodes) == 0 {
		return nil, invalid(`the document holds no nodes — expected a JSON object with a "nodes" array`)
	}

	// Stamp on copies: the caller's document is left as it was.
	nodes := make([]designer.PatchNode, len(doc.Nodes))
	copy(nodes, doc.Nodes)
	missing, err := stampIdentities(ctx, store, nodes, doc.Plugins)
	if err != nil {
		return nil, err
	}

	graph, problems := designer.PlanPatch(designer.Patch{Nodes: nodes, Edges: doc.Edges, Notes: doc.Notes}, nil)
	if problems == nil {
		problems = []designer.Problem{}
	}
	if starts := countType(graph, inflow.NODE_START); starts != 1 {
		return nil, invalid(`the workflow must contain exactly one node of kind "startNode"`)
	}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = strings.TrimSpace(doc.Title)
	}
	if title == "" {
		title = "Imported workflow"
	}
	// Re-attach profiles the document carried by reference; see the function.
	resolveSettingsProfiles(ctx, store, &graph)

	rec := models.FlowRecord{ID: strings.TrimSpace(in.ID), Title: title, ViewFlow: graph}
	inflow.NormalizeGraph(&rec)

	res := &Result{Problems: problems, MissingActions: missing, DryRun: in.DryRun}
	if startID, compiled, err := inflow.FLowCompiler(rec); err != nil {
		res.CompileError = err.Error()
	} else {
		res.StartNodeID, res.Compiled = startID, compiled
	}
	if !in.DryRun {
		if err := store.Workflows().Upsert(ctx, &rec); err != nil {
			return nil, err
		}
	}
	res.Flow = rec
	return res, nil
}

// Export writes a saved flow as a portable document: the graph as a designer
// patch (designer.GraphToPatch) under the header, with a plugin manifest naming
// each imported plugin the graph reaches — by the actions it uses, plus the
// plugin's name and repo from this install's registration row when it has one.
// The manifest is best-effort: a plugin the install no longer lists is still
// named by its action namespace, and an unreadable extension table just leaves
// repos out. Nothing here can fail.
// resolveSettingsProfiles re-attaches the settings profile a document carried by
// REFERENCE. Export keeps only `settingsId` and strips the resolved values
// because they hold provider tokens (see designer.Export), while the compiler
// reads `data.settings` and nothing else (inflow/node_builders.go). So a node
// that arrives without this step reaches its plugin with an EMPTY settings map,
// and the plugin correctly refuses the job on missing required fields — the
// failure looks like a broken plugin rather than an unresolved reference.
//
// The editor already does this on its own import path (resolveImportedProfiles
// in WorkflowEditorView.vue). Doing it here covers every server-side road in:
// flo_import_workflow, the designer's flo_plan_patch / flo_apply_patch, and
// POST /flow/import — none of which pass through the editor.
//
// An id this install does not have leaves the node with empty settings instead
// of failing the import, matching how a missing plugin action is kept and
// flagged: the operator picks a profile in the drawer.
func resolveSettingsProfiles(ctx context.Context, store repository.Store, graph *compiler.VueFlow) {
	cache := map[string]*models.NodeSetting{}
	for _, n := range graph.Nodes {
		// Data is a map, so mutating it here reaches the node in the graph even
		// though the range variable is a copy of the struct.
		data, ok := n.Data.(map[string]any)
		if !ok {
			continue
		}
		id, _ := data["settingsId"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		prof, seen := cache[id]
		if !seen {
			// A lookup failure is indistinguishable from "this install has no such
			// profile" here, and both want the same outcome: leave it for the drawer.
			prof, _ = store.NodeSettings().GetByID(ctx, id)
			cache[id] = prof
		}
		if prof == nil {
			data["settingsName"] = ""
			data["settings"] = map[string]any{}
			continue
		}
		data["settingsName"] = prof.Title
		settings := make(map[string]any, len(prof.Settings))
		for k, v := range prof.Settings {
			settings[k] = v
		}
		data["settings"] = settings
	}
}

func Export(ctx context.Context, store repository.Store, rec models.FlowRecord) Document {
	patch := designer.GraphToPatch(rec.ViewFlow)
	title := strings.TrimSpace(rec.Title)
	if title == "" {
		title = "Untitled workflow"
	}
	return Document{
		Flomorphic: &Header{Kind: "workflow", Version: Version, ExportedAt: time.Now().UTC().Format(time.RFC3339)},
		Title:      title,
		Plugins:    pluginManifest(ctx, store, rec.ViewFlow),
		Nodes:      patch.Nodes,
		Edges:      patch.Edges,
	}
}

// pluginManifest is one entry per plugin the graph's `plugin` nodes reach,
// grouped by the install-local pluginId (so one plugin's methods land in one
// entry) — but only the portable `actions` are written, never the id. Mirrors
// the wapp's pluginManifest.
func pluginManifest(ctx context.Context, store repository.Store, graph compiler.VueFlow) []PluginManifestEntry {
	type group struct {
		actions map[string]bool
		first   int
	}
	groups := map[string]*group{}
	var order []string
	for _, n := range graph.Nodes {
		if n.Type != "plugin" {
			continue
		}
		data, _ := n.Data.(map[string]any)
		pluginID := strings.TrimSpace(asString(data["pluginId"]))
		action := strings.TrimSpace(asString(data["action"]))
		key := pluginID
		if key == "" {
			key = action
		}
		if key == "" {
			continue
		}
		g := groups[key]
		if g == nil {
			g = &group{actions: map[string]bool{}, first: len(order)}
			groups[key] = g
			order = append(order, key)
		}
		if action != "" {
			g.actions[action] = true
		}
	}
	if len(order) == 0 {
		return nil
	}

	// The registration row (Action == "") of each plugin names it and says where
	// its source lives.
	regs := map[string]*models.ExtensionRecord{}
	if store != nil {
		if rows, _, err := store.Extensions().List(ctx, repository.ListParams{Kind: string(models.KindExtension), Limit: 500}); err == nil {
			for i := range rows {
				if rows[i].Action == "" && rows[i].PluginID != "" {
					regs[rows[i].PluginID] = &rows[i]
				}
			}
		}
	}

	out := make([]PluginManifestEntry, 0, len(order))
	for _, key := range order {
		g := groups[key]
		actions := make([]string, 0, len(g.actions))
		for a := range g.actions {
			actions = append(actions, a)
		}
		sort.Strings(actions)
		entry := PluginManifestEntry{Actions: actions}
		if reg := regs[key]; reg != nil {
			entry.Name = reg.Name
			entry.Repo, entry.Ref, entry.Subdir = reg.Install.Repo, reg.Install.Ref, reg.Install.Subdir
		}
		if entry.Name == "" {
			if len(actions) > 0 {
				entry.Name = namespaceOf(actions[0])
			} else {
				entry.Name = key
			}
		}
		out = append(out, entry)
	}
	return out
}

// stampIdentities gives every plugin-backed patch node the extension identity
// this install would attach when the node is dropped from the palette:
//   - a `plugin` node is matched by its action method (the portable key) to a
//     synced action row → extensionId / pluginId / form / outbound. A pluginId the
//     node already carries is honoured as a same-install hint when a row with that
//     exact (pluginId, action) exists — the way the wapp's stampPluginRef does —
//     so two local plugins exposing one method name still land on the intended one;
//   - a builtin plugin-backed kind (llm, mcp, cast, http…) gets its builtin row's
//     extensionId / pluginId by node type.
//
// Any identity the document carried is cleared first: a foreign pluginId
// compiles into a NATS subject nothing answers on. The returned list is the
// plugin actions no local plugin provides, each with the refs of its callers.
func stampIdentities(ctx context.Context, store repository.Store, nodes []designer.PatchNode, manifest []PluginManifestEntry) ([]MissingAction, error) {
	exts, _, err := store.Extensions().List(ctx, repository.ListParams{Kind: string(models.KindExtension), Limit: 500})
	if err != nil {
		return nil, err
	}
	builtins, _, err := store.Extensions().List(ctx, repository.ListParams{Kind: string(models.KindBuiltin), Limit: 200})
	if err != nil {
		return nil, err
	}
	byAction := map[string]*models.ExtensionRecord{}
	byPluginAction := map[string]*models.ExtensionRecord{}
	for i := range exts {
		row := &exts[i]
		if row.Action == "" || row.PluginID == "" {
			continue
		}
		if _, dup := byAction[row.Action]; !dup {
			byAction[row.Action] = row
		}
		byPluginAction[row.PluginID+"\x00"+row.Action] = row
	}
	byType := map[string]*models.ExtensionRecord{}
	for i := range builtins {
		row := &builtins[i]
		if _, dup := byType[string(row.Type)]; !dup {
			byType[string(row.Type)] = row
		}
	}

	missingByAction := map[string]*MissingAction{}
	var order []string
	for i := range nodes {
		n := &nodes[i]
		// Copy the data map: the caller's document must not see the stamps.
		data := make(map[string]any, len(n.Data)+4)
		for k, v := range n.Data {
			data[k] = v
		}
		n.Data = data

		label := strings.TrimSpace(n.Ref)
		if label == "" {
			label = strings.TrimSpace(n.Title)
		}
		if label == "" {
			label = fmt.Sprintf("node%d", i+1)
		}

		if n.Kind == "plugin" {
			action := strings.TrimSpace(asString(data["action"]))
			hint := strings.TrimSpace(asString(data["pluginId"]))
			delete(data, "extensionId")
			delete(data, "pluginId")
			delete(data, "form")
			delete(data, "outbound")
			delete(data, "missingPlugin")
			if action == "" {
				continue
			}
			row := byPluginAction[hint+"\x00"+action]
			if row == nil {
				row = byAction[action]
			}
			if row != nil {
				data["extensionId"] = row.ID
				data["pluginId"] = row.PluginID
				data["action"] = row.Action
				if strings.TrimSpace(n.Title) == "" && strings.TrimSpace(asString(data["title"])) == "" {
					data["title"] = row.Name
				}
				data["form"] = map[string]any{"schema": orEmpty(row.Parameters.Schema), "ui": orEmpty(row.Parameters.UI)}
				if len(row.Outbound) > 0 {
					data["outbound"] = outboundRows(row.Outbound)
				}
				continue
			}
			// Unresolved: keep the node, mark it the way the canvas expects, and
			// report the action once with the nodes that call it.
			entry := manifestFor(manifest, action)
			data["missingPlugin"] = map[string]any{
				"name": entry.Name, "repo": entry.Repo, "ref": entry.Ref, "subdir": entry.Subdir,
			}
			m := missingByAction[action]
			if m == nil {
				m = &MissingAction{Action: action, Plugin: entry.Name, Repo: entry.Repo, Ref: entry.Ref, Subdir: entry.Subdir}
				missingByAction[action] = m
				order = append(order, action)
			}
			m.Nodes = append(m.Nodes, label)
			continue
		}
		if row := byType[n.Kind]; row != nil {
			data["extensionId"] = row.ID
			if row.PluginID != "" {
				data["pluginId"] = row.PluginID
			}
		}
	}
	out := make([]MissingAction, 0, len(order))
	for _, a := range order {
		out = append(out, *missingByAction[a])
	}
	return out, nil
}

// outboundRows is a row's declared ports as the generic rows the planner's
// derivedPorts reads (and the canvas stores): [{title, tags, description?}].
func outboundRows(ports []models.OutboundPort) []any {
	out := make([]any, 0, len(ports))
	for _, p := range ports {
		tags := make([]any, 0, len(p.Tags))
		for _, t := range p.Tags {
			tags = append(tags, t)
		}
		row := map[string]any{"title": p.Title, "tags": tags}
		if p.Description != "" {
			row["description"] = p.Description
		}
		out = append(out, row)
	}
	return out
}

// manifestFor names the plugin behind an action from the document's own
// manifest, falling back to the action's namespace (`github` of
// `github.issues.create`).
func manifestFor(manifest []PluginManifestEntry, action string) PluginManifestEntry {
	for _, p := range manifest {
		for _, a := range p.Actions {
			if a == action {
				if p.Name == "" {
					p.Name = namespaceOf(action)
				}
				return p
			}
		}
	}
	return PluginManifestEntry{Name: namespaceOf(action)}
}

func namespaceOf(action string) string {
	if i := strings.IndexByte(action, '.'); i > 0 {
		return action[:i]
	}
	if action == "" {
		return "plugin"
	}
	return action
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func countType(g compiler.VueFlow, typ string) int {
	n := 0
	for _, node := range g.Nodes {
		if node.Type == typ {
			n++
		}
	}
	return n
}
