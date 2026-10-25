package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/flowfile"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// documentGuide names the shape flo_import_workflow takes and flo_export_workflow
// returns — the portable workflow document (package flowfile). It is the same
// graph patch flo_plan_patch / flo_apply_patch author, under a small header, so
// the full design guidance is flo_get_design_guide; this only names the shape.
const documentGuide = `The portable workflow document is the file the editor's Export writes and the flow-cookbook ships — a designer GRAPH PATCH under a small header:
{
  "flomorphic": {"kind": "workflow", "version": 1},   // optional header
  "title": "…",
  "plugins": [{"name", "actions": ["ns.method", …], "repo"?, "ref"?, "subdir"?}],  // optional: the imported plugins the flow depends on, keyed by action names
  "preferences": {"entry": {…}},                       // optional: the input the flow asks for before it runs (see flo_get_flow_entry)
  "nodes": [{ref, kind, title, key?, scope?, position?, data?}],   // one node MUST be kind "startNode"
  "edges": [{from, to, port?}]                                      // refs, and a port name where the source has derived ports
}
It carries NO install-local identity: no canvas ids, no extensionId / pluginId (a per-install address), no plugin form/outbound, no resolved settings (secrets). A plugin node is identified by its "action" method alone; import re-stamps this install's identity from its own extension table and reports the actions no local plugin provides as "missingActions" (the node is kept and flagged, not dropped). Any object with a "nodes" array is accepted, header or not — so a bare patch is a valid document.
This is about half the size of the saved FlowRecord (which carries Vue-Flow render state) and is the form to read, edit and re-import a flow in. Call flo_get_design_guide before authoring one from scratch.`

// entryGuide is the contract for a flow's ENTRY FORM — the input a flow asks to
// have filled in before it runs. It is quoted into every tool that reads or
// writes one, because the capability is only useful if a client discovers it
// without being told: a flow that declares an entry must be launched WITH its
// values, and a flow being authored can declare one instead of growing a JS
// node whose only job is to seed the context.
const entryGuide = `A flow may declare an ENTRY FORM — the input it needs before it runs — as part of its preferences:
{
  "entry": {
    "title": "Order to review", "description": "…",      // optional, heads the form
    "form": {
      "schema": {…},       // EITHER a JSON Schema (fields, defaults, enums, required) OR a plain JSON object of the values to seed
      "ui": {…}            // optional JSON Forms UI schema (only meaningful for a JSON Schema)
    },
    "target": "$._entry",  // the ONE context location the answers are written to; default $._entry
    "prefill": "default",  // "default" = the schema's own defaults | "live" = what already sits at the target in the run's context
    "required": false      // true refuses a launch that supplies nothing and finds nothing at the target
  }
}
The two ways to write "schema" are deliberate: a JSON Schema gets the full form renderer (the same plugin form-builder the plugin dialogs use), while a plain JSON object is the easy way in — it IS the values to seed, shown as a flat key/value table. A document counts as a schema when it declares $schema or a non-empty "properties".
Seeding is surgical: the submitted values are written at "target" and NOTHING else in the context document is touched — no document is replaced and no blank context is created. The target is a plain member path ($, $._entry, $.input.order, $['order id']); missing objects along it are created. Array, wildcard and filter paths ($.items[0], $.items[*], $..id) are refused, because a write needs exactly one destination.
This is the declarative replacement for the pattern of wiring a JS node after the start node to put a fixed shape into the context. Flow nodes read the seeded data the way they read anything else — {{$._entry.<field>}}.
CAREFUL when a flow DEPENDS on an entry field: filling the form is the launcher's choice, so a value can arrive absent (no default, nothing submitted — a trigger or schedule launch supplies nothing) or null/empty (a field the person cleared). Design the flow to check rather than to assume: a js node right after the start node that reads _get("$._entry"), supplies the fallbacks and routes a missing field to a reporting branch, with the rest of the graph depending on THAT node's output. "required": true refuses an empty launch, but it does not make any individual field non-null.
Launching: read the form with flo_get_flow_entry (it returns the fields' starting values too), then pass the filled values as flo_start_process's "entry". A flow that declares no entry answers entry:null and launches as it always did.`

// registerWorkflowTools exposes the /flow surface: read (list, get with an
// optional compile pass) and the portable-document road in and out (import /
// export). Authoring goes through the designer tools (flo_plan_patch /
// flo_apply_patch) or an import; both land through the same planner, identity
// stamping and upsert the editor uses, so an MCP-landed flow is identical to a
// hand-drawn one.
func registerWorkflowTools(s *server.MCPServer, store repository.Store) {
	repo := store.Workflows()

	s.AddTool(mcp.NewTool("flo_list_workflows",
		withOpts(pageArgs(),
			mcp.WithDescription("List saved workflows (FlowRecord), newest first, paginated. `tags` narrows the list to flows carrying at least one of the given labels."),
			mcp.WithString("tags", mcp.Description("optional comma-separated labels; a flow is kept when it carries at least one of them")),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, total, err := repo.List(ctx, listParams(req))
		if err != nil {
			return repoError(err, "workflows not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_workflow",
		mcp.WithDescription("Get one workflow by id as the saved FlowRecord (the raw Vue-Flow graph the editor renders — verbose). To READ or EDIT a flow's design prefer flo_export_workflow, which returns the compact portable document. Set compile=true to also return the lowered inflow node graph (what the flow compiles to on the engine)."),
		mcp.WithString("id", mcp.Required(), mcp.Description("workflow id (flow_…)")),
		mcp.WithBoolean("compile", mcp.Description("also return the compiled node graph")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "workflow not found")
		}
		if !req.GetBool("compile", false) {
			return jsonResult(rec)
		}
		startNodeID, nodes, err := inflow.FLowCompiler(*rec)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("compile failed", err), nil
		}
		return jsonResult(map[string]any{"flow": rec, "startNodeId": startNodeID, "nodes": nodes})
	})

	s.AddTool(mcp.NewTool("flo_export_workflow",
		mcp.WithDescription("Export a saved workflow as the portable workflow document — the readable, compact form (a designer graph patch under a small header) with all install-local identity stripped. This is the form to read a flow's design in, to edit and hand back to flo_import_workflow, or to save as a file / ship in a package.\n\n"+documentGuide),
		mcp.WithString("id", mcp.Required(), mcp.Description("workflow id (flow_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "workflow not found")
		}
		return jsonResult(flowfile.Export(ctx, store, *rec))
	})

	s.AddTool(mcp.NewTool("flo_import_workflow",
		mcp.WithDescription("Import a portable workflow document as a workflow on this install — the same road as the editor's Import dialog and the REST POST /flow/import. Plugin nodes are re-stamped with this install's extension identity by their action; actions no local plugin provides are kept, flagged, and listed in missingActions so you can tell the operator what to install. The graph is compile-checked (compileError is reported, not a gate — a plugin's account resolves live) and saved unless dryRun. Use dryRun=true to validate a document without saving.\n\n"+documentGuide),
		mcp.WithObject("workflow", mcp.Required(), mcp.Description("the workflow document: {flomorphic?, title?, plugins?, nodes[], edges[]}")),
		mcp.WithString("id", mcp.Description("workflow id to overwrite (a re-install); empty creates a new one")),
		mcp.WithString("title", mcp.Description("overrides the document's own title")),
		mcp.WithArray("tags", mcp.Description("overrides the document's own tags — the labels a flow list is filtered by; omit to keep the document's")),
		mcp.WithBoolean("dryRun", mcp.Description("plan, stamp and compile-check without saving")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			Workflow json.RawMessage `json:"workflow"`
			ID       string          `json:"id"`
			Title    string          `json:"title"`
			Tags     []string        `json:"tags"`
			DryRun   bool            `json:"dryRun"`
		}
		if err := req.BindArguments(&in); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid import payload", err), nil
		}
		doc, err := flowfile.Parse(in.Workflow)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, bad := importDocument(ctx, store, flowfile.Input{Doc: doc, ID: in.ID, Title: in.Title, Tags: in.Tags, DryRun: in.DryRun})
		if bad != nil {
			return bad, nil
		}
		return jsonResult(map[string]any{
			"ok":             true,
			"flow":           res.Flow,
			"problems":       res.Problems,
			"missingActions": res.MissingActions,
			"unboundNodes":   res.UnboundNodes,
			"compileError":   res.CompileError,
			"dryRun":         res.DryRun,
		})
	})

	s.AddTool(mcp.NewTool("flo_get_flow_entry",
		mcp.WithDescription("Get a flow's ENTRY FORM resolved for a launch: the form it declares, the context path its answers are written to, and the values its fields should start at. CALL THIS BEFORE flo_start_process — a flow that declares an entry expects its values, and one marked required refuses to run without them. `entry: null` means the flow asks for nothing.\n\n"+entryGuide),
		mcp.WithString("id", mcp.Required(), mcp.Description("workflow id (flow_…)")),
		mcp.WithString("contextId", mcp.Description("the context the run would go against — what a `live` prefill reads its starting values from")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "workflow not found")
		}
		return jsonResult(inflow.ResolveEntryView(ctx, store, *rec, strings.TrimSpace(req.GetString("contextId", ""))))
	})

	s.AddTool(mcp.NewTool("flo_set_flow_preferences",
		mcp.WithDescription("Set (or clear) a flow's preferences — today its entry form — without resending its graph. Pass the preferences object to set it; pass an empty object to clear it. Use this to give an existing flow the input form it was collecting with a JS node after the start node.\n\n"+entryGuide),
		mcp.WithString("id", mcp.Required(), mcp.Description("workflow id (flow_…)")),
		mcp.WithObject("preferences", mcp.Required(), mcp.Description(`{"entry": {title?, description?, form: {schema, ui?}, target?, prefill?, required?}} — or {} to clear`)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			ID          string                  `json:"id"`
			Preferences *models.FlowPreferences `json:"preferences"`
		}
		if err := req.BindArguments(&in); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid preferences payload", err), nil
		}
		if strings.TrimSpace(in.ID) == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		rec, err := repo.GetByID(ctx, in.ID)
		if err != nil {
			return repoError(err, "workflow not found")
		}
		prefs := in.Preferences
		if prefs == nil {
			prefs = &models.FlowPreferences{}
		}
		prefs.Normalize()
		// A target that does not name exactly one context location would only
		// surface as a failed run later, so it is refused here (the schema
		// itself is the launch's business — a form may be saved half-written).
		if entry := prefs.EntryOrNil(); entry != nil {
			if _, err := models.ParseContextPath(entry.TargetPath()); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}
		rec.Preferences = prefs
		if err := repo.Upsert(ctx, rec); err != nil {
			return repoError(err, "workflow not saved")
		}
		wslog.Emit("flow.changed", map[string]any{"id": rec.ID, "source": "mcp"})
		return jsonResult(map[string]any{"id": rec.ID, "preferences": rec.Preferences})
	})
}

// importDocument runs flowfile.Import for a tool and turns its failures into
// ready tool-error results, emitting the live-sync event on a real save (an
// open editor refetches on it — see api/wslog). Shared by flo_import_workflow
// and the designer's flo_plan_patch / flo_apply_patch.
func importDocument(ctx context.Context, store repository.Store, in flowfile.Input) (*flowfile.Result, *mcp.CallToolResult) {
	res, err := flowfile.Import(ctx, store, in)
	if err != nil {
		if errors.Is(err, flowfile.ErrInvalid) {
			return nil, mcp.NewToolResultError(strings.TrimPrefix(err.Error(), flowfile.ErrInvalid.Error()+": "))
		}
		bad, _ := repoError(err, "workflow not saved")
		return nil, bad
	}
	if !res.DryRun {
		wslog.Emit("flow.changed", map[string]any{"id": res.Flow.ID, "source": "mcp"})
	}
	return res, nil
}
