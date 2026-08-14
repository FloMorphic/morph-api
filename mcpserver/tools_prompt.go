package mcpserver

import (
	"context"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPromptTools exposes the /prompt surface: list, get, upsert, delete.
// Adding a prompt template through MCP is one of the primary use cases.
func registerPromptTools(s *server.MCPServer, store repository.Store) {
	repo := store.Prompts()

	s.AddTool(mcp.NewTool("flo_list_prompts",
		withOpts(pageArgs(),
			mcp.WithDescription("List prompt templates, newest first, paginated."),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, total, err := repo.List(ctx, listParams(req))
		if err != nil {
			return repoError(err, "prompts not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_prompt",
		mcp.WithDescription("Get one prompt template by id — its template text, documented {{variables}}, and tags."),
		mcp.WithString("id", mcp.Required(), mcp.Description("prompt id (prompt_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "prompt not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_upsert_prompt",
		mcp.WithDescription("Create (empty id) or update a prompt template. `template` is the text with {{variable}} placeholders; `variables` documents them; `tags` group it."),
		mcp.WithString("id", mcp.Description("prompt id; empty creates a new one")),
		mcp.WithString("title", mcp.Required(), mcp.Description("prompt title")),
		mcp.WithString("description", mcp.Description("what the prompt is for")),
		mcp.WithString("template", mcp.Required(), mcp.Description("prompt text with {{variable}} placeholders")),
		mcp.WithArray("variables", mcp.Description("placeholder descriptors: [{name, description?, default?, required?}]"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"default":     map[string]any{"type": "string"},
					"required":    map[string]any{"type": "boolean"},
				},
			}),
		),
		mcp.WithArray("tags", mcp.Description("free-form labels"), mcp.WithStringItems()),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var rec models.PromptRecord
		if err := req.BindArguments(&rec); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid prompt payload", err), nil
		}
		if err := repo.Upsert(ctx, &rec); err != nil {
			return repoError(err, "prompt not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_delete_prompt",
		mcp.WithDescription("Delete a prompt template by id."),
		mcp.WithString("id", mcp.Required(), mcp.Description("prompt id (prompt_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		if err := repo.Delete(ctx, id); err != nil {
			return repoError(err, "prompt not found")
		}
		return jsonResult(map[string]any{"id": id, "deleted": true})
	})
}
