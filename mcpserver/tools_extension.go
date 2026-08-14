package mcpserver

import (
	"context"
	"strings"

	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerExtensionTools exposes the read side of the /extension surface — the
// node palette. Every row is a palette node (builtins and imported inflowv1
// plugins). The live inflowv1 proxy calls (intro/settings/actions/forms, sync,
// install, cred) need NATS and a plugin runtime and are deliberately left to the
// REST API; MCP just browses the catalog.
func registerExtensionTools(s *server.MCPServer, store repository.Store) {
	repo := store.Extensions()

	s.AddTool(mcp.NewTool("flo_list_extensions",
		withOpts(pageArgs(),
			mcp.WithDescription("List palette extensions (nodes), newest first. Filter by kind."),
			mcp.WithString("kind", mcp.Description("builtin|extension")),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p := listParams(req)
		p.Kind = strings.TrimSpace(req.GetString("kind", ""))
		items, total, err := repo.List(ctx, p)
		if err != nil {
			return repoError(err, "extensions not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_extension",
		mcp.WithDescription("Get one palette extension by id — its kind, plugin identity, parameters and bindings."),
		mcp.WithString("id", mcp.Required(), mcp.Description("extension id (ext_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "extension not found")
		}
		return jsonResult(rec)
	})
}
