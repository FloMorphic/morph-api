package mcpserver

import (
	"context"
	"strings"

	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerTriggerTools exposes the read side of the /trigger surface — the
// inbound webhooks and recurring schedules that launch a flow, including the
// bounded delivery/fire log kept on each. Creating a trigger wires a
// secret-bearing webhook or a live schedule, so authoring is left to the REST
// CRUD group; MCP inspects them.
func registerTriggerTools(s *server.MCPServer, store repository.Store) {
	repo := store.Triggers()

	s.AddTool(mcp.NewTool("flo_list_triggers",
		withOpts(pageArgs(),
			mcp.WithDescription("List workflow triggers (webhooks and schedules), newest first. Filter by flowId or kind."),
			mcp.WithString("flowId", mcp.Description("only triggers of this workflow")),
			mcp.WithString("kind", mcp.Description("webhook|schedule")),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p := listParams(req)
		p.FlowID = strings.TrimSpace(req.GetString("flowId", ""))
		p.Kind = strings.TrimSpace(req.GetString("kind", ""))
		items, total, err := repo.List(ctx, p)
		if err != nil {
			return repoError(err, "triggers not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_trigger",
		mcp.WithDescription("Get one trigger by id — its config and recent delivery/fire log."),
		mcp.WithString("id", mcp.Required(), mcp.Description("trigger id")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "trigger not found")
		}
		return jsonResult(rec)
	})
}
