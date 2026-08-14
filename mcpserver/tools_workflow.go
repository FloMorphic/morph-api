package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/api/wslog"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// nodeDataGuide documents, for the authoring tools, the type-specific fields
// each node's `data` object carries beyond the shared title/key/scope — the
// contract the compiler's buildXxx hooks read (see inflow/node_builders.go). The
// param form of every builtin (and a plugin action's live @form) is also
// available through flo_list_extensions / flo_get_extension, which is the
// authoritative per-node schema; this is the quick reference.
const nodeDataGuide = `Every node.data has: title (required), and optional key (binds the node's output into context as $.<key>) and scope.
One node MUST be type "startNode". Node types and their extra data fields:
- startNode / promissall / void: none (promissall is a fan-in join; wire every branch into it)
- llm:      provider, model, settingsId, messages/prompt config
- mcp:      url, transport, auth, tool, arguments (MCP client node)
- http:     method, url, headers, body
- cast:     body (map of field->value/expression)
- rule / contract: lang ("cel"|"opa"), logic_rule, opa_result
- js / opa: lang ("js"|"opa"), logic_rule, opa_result
- hitl:     mode ("park"|"continue"), channel, prompt, settingsId, key
- docstore / vecstore: action, storeId, and the action's fields
- until:    the schedule/continue config
- plugin:   the imported plugin action's fields (from its @form)
- goto:     goto {flowId, nodeId}
Edges wire nodes: {id, source, target, sourceHandle?, targetHandle?, data:{tags:[]}}. Branch routing uses data.tags.
Use flo_list_extensions for the exact param form of each node type, and flo_compile_workflow to validate a draft before saving.`

// registerWorkflowTools exposes the /flow surface: read (list, get with an
// optional compile pass) and authoring (compile a candidate graph to validate
// it, then upsert). A workflow is a Vue-Flow graph (nodes + edges); the tools
// reuse the same compiler and upsert the visual editor does, so an MCP-authored
// flow is identical to a hand-drawn one.
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
		mcp.WithDescription("Get one workflow by id. Set compile=true to also return the lowered inflow node graph (what the flow compiles to on the engine)."),
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

	s.AddTool(mcp.NewTool("flo_compile_workflow",
		mcp.WithDescription("Validate a CANDIDATE (unsaved) workflow graph by running the inflow compiler over it. Returns the lowered node graph on success, or the compile error, without saving anything. Use it to check a draft before flo_upsert_workflow.\n\n"+nodeDataGuide),
		mcp.WithString("title", mcp.Description("optional title (not required to compile)")),
		mcp.WithArray("nodes", mcp.Required(), mcp.Description("Vue-Flow nodes: [{id, type, data:{title, key?, ...}, position?:{x,y}}]"),
			mcp.Items(map[string]any{"type": "object"})),
		mcp.WithArray("edges", mcp.Description("Vue-Flow edges: [{id?, source, target, sourceHandle?, targetHandle?, data:{tags:[]}}]"),
			mcp.Items(map[string]any{"type": "object"})),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rec, bad := parseFlowInput(req)
		if bad != nil {
			return bad, nil
		}
		startNodeID, nodes, err := inflow.FLowCompiler(*rec)
		if err != nil {
			return jsonResult(map[string]any{"ok": false, "error": err.Error()})
		}
		return jsonResult(map[string]any{"ok": true, "startNodeId": startNodeID, "nodes": nodes})
	})

	s.AddTool(mcp.NewTool("flo_upsert_workflow",
		mcp.WithDescription("Create (empty id) or update a workflow from a Vue-Flow graph — the same save the visual editor performs. Saving does not require the graph to compile (compile separately with flo_compile_workflow); it only checks the graph is structurally sound (node ids, one startNode, each node has a data.title).\n\n"+nodeDataGuide),
		mcp.WithString("id", mcp.Description("workflow id; empty creates a new one")),
		mcp.WithString("title", mcp.Required(), mcp.Description("workflow title")),
		mcp.WithArray("nodes", mcp.Required(), mcp.Description("Vue-Flow nodes: [{id, type, data:{title, key?, ...}, position?:{x,y}}]"),
			mcp.Items(map[string]any{"type": "object"})),
		mcp.WithArray("edges", mcp.Description("Vue-Flow edges: [{id?, source, target, sourceHandle?, targetHandle?, data:{tags:[]}}]"),
			mcp.Items(map[string]any{"type": "object"})),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rec, bad := parseFlowInput(req)
		if bad != nil {
			return bad, nil
		}
		if strings.TrimSpace(rec.Title) == "" {
			return mcp.NewToolResultError("title is required"), nil
		}
		// Shared with the REST /flow upsert: fill headless-author graph defaults so
		// an MCP-built flow renders in the editor, using the one authoritative
		// normalizer (no drift from the web app's own save path).
		inflow.NormalizeGraph(rec)
		if err := repo.Upsert(ctx, rec); err != nil {
			return repoError(err, "workflow not found")
		}
		// Live sync: an open editor refetches on this event (see api/wslog).
		wslog.Emit("flow.changed", map[string]any{"id": rec.ID, "source": "mcp"})
		return jsonResult(rec)
	})
}

// parseFlowInput reads the {id?, title, nodes[], edges[]} authoring arguments
// into a FlowRecord and runs the light structural checks the compiler assumes
// (it reads node.data["title"] as a bare string and needs a startNode). On
// failure it returns a ready tool-error result. It does NOT compile — that is
// flo_compile_workflow's job — so a partial draft can still be saved, matching
// the visual editor where save and compile are separate.
func parseFlowInput(req mcp.CallToolRequest) (*models.FlowRecord, *mcp.CallToolResult) {
	var in struct {
		ID    string                 `json:"id"`
		Title string                 `json:"title"`
		Nodes []compiler.VueFlowNode `json:"nodes"`
		Edges []compiler.Edges       `json:"edges"`
	}
	if err := req.BindArguments(&in); err != nil {
		return nil, mcp.NewToolResultErrorFromErr("invalid workflow payload", err)
	}
	if len(in.Nodes) == 0 {
		return nil, mcp.NewToolResultError("a workflow needs at least one node")
	}
	starts := 0
	for i, n := range in.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return nil, mcp.NewToolResultError(fmt.Sprintf("node %d is missing an id", i))
		}
		if strings.TrimSpace(n.Type) == "" {
			return nil, mcp.NewToolResultError(fmt.Sprintf("node %q is missing a type", n.ID))
		}
		data, ok := n.Data.(map[string]any)
		if !ok {
			return nil, mcp.NewToolResultError(fmt.Sprintf("node %q data must be an object with a title", n.ID))
		}
		if title, ok := data["title"].(string); !ok || strings.TrimSpace(title) == "" {
			return nil, mcp.NewToolResultError(fmt.Sprintf("node %q data.title must be a non-empty string", n.ID))
		}
		if n.Type == inflow.NODE_START {
			starts++
		}
	}
	if starts == 0 {
		return nil, mcp.NewToolResultError(`the graph needs exactly one node of type "startNode"`)
	}
	return &models.FlowRecord{
		ID:       in.ID,
		Title:    in.Title,
		ViewFlow: compiler.VueFlow{Nodes: in.Nodes, Edges: in.Edges},
	}, nil
}
