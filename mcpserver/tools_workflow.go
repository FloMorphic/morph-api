package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/flowfile"
	"github.com/FloMorphic/morph-api/inflow"
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
  "nodes": [{ref, kind, title, key?, scope?, position?, data?}],   // one node MUST be kind "startNode"
  "edges": [{from, to, port?}]                                      // refs, and a port name where the source has derived ports
}
It carries NO install-local identity: no canvas ids, no extensionId / pluginId (a per-install address), no plugin form/outbound, no resolved settings (secrets). A plugin node is identified by its "action" method alone; import re-stamps this install's identity from its own extension table and reports the actions no local plugin provides as "missingActions" (the node is kept and flagged, not dropped). Any object with a "nodes" array is accepted, header or not — so a bare patch is a valid document.
This is about half the size of the saved FlowRecord (which carries Vue-Flow render state) and is the form to read, edit and re-import a flow in. Call flo_get_design_guide before authoring one from scratch.`

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
			mcp.WithDescription("List saved workflows (FlowRecord), newest first, paginated."),
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
		mcp.WithBoolean("dryRun", mcp.Description("plan, stamp and compile-check without saving")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			Workflow json.RawMessage `json:"workflow"`
			ID       string          `json:"id"`
			Title    string          `json:"title"`
			DryRun   bool            `json:"dryRun"`
		}
		if err := req.BindArguments(&in); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid import payload", err), nil
		}
		doc, err := flowfile.Parse(in.Workflow)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, bad := importDocument(ctx, store, flowfile.Input{Doc: doc, ID: in.ID, Title: in.Title, DryRun: in.DryRun})
		if bad != nil {
			return bad, nil
		}
		return jsonResult(map[string]any{
			"ok":             true,
			"flow":           res.Flow,
			"problems":       res.Problems,
			"missingActions": res.MissingActions,
			"compileError":   res.CompileError,
			"dryRun":         res.DryRun,
		})
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
