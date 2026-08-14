package mcpserver

import (
	"context"

	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerWorkflowTools exposes the /flow read surface: list and get (with an
// optional compile pass). Authoring a workflow means producing a full Vue-Flow
// graph, which belongs in the visual editor, so there is deliberately no upsert
// tool here — an MCP client inspects flows and launches runs of them.
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
}
