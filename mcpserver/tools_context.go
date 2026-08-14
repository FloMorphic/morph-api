package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerContextTools exposes the /context surface. A context document is the
// shared state a run reads and writes, so `flo_get_context` is how an MCP client
// inspects what a process produced, and `flo_upsert_context` seeds or edits it.
func registerContextTools(s *server.MCPServer, store repository.Store) {
	repo := store.Contexts()

	s.AddTool(mcp.NewTool("flo_list_contexts",
		withOpts(pageArgs(),
			mcp.WithDescription("List context documents, newest first, paginated."),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, total, err := repo.List(ctx, listParams(req))
		if err != nil {
			return repoError(err, "contexts not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_context",
		mcp.WithDescription("Get one context document by id — its title, the JSON context object a run read/wrote, header metadata, and who last changed it. Use this to see what a process produced."),
		mcp.WithString("id", mcp.Required(), mcp.Description("context id (ctx_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "context not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_upsert_context",
		mcp.WithDescription("Create (empty id) or update a context document. `context` is the state object; pass it as a JSON object. The change is attributed to the API."),
		mcp.WithString("id", mcp.Description("context id; empty creates a new one")),
		mcp.WithString("title", mcp.Description("context title")),
		mcp.WithObject("context", mcp.Description("the context state as a JSON object")),
		mcp.WithObject("header", mcp.Description("optional free-form metadata")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rec := models.ContextRecord{
			ID:    req.GetString("id", ""),
			Title: strings.TrimSpace(req.GetString("title", "")),
		}
		if rec.Title == "" {
			rec.Title = "untitled context"
		}
		if obj, err := argObject(req, "context"); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid context", err), nil
		} else if obj != nil {
			// The store persists `context` as a JSON string (see context/crud.go),
			// so serialize the object the client handed us.
			b, _ := json.Marshal(obj)
			rec.Context = string(b)
		}
		if hdr, err := argObject(req, "header"); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid header", err), nil
		} else {
			rec.Header = hdr
		}
		rec.UpdatedBy = models.LastChange{By: models.ByAPI}
		if err := repo.Upsert(ctx, &rec); err != nil {
			return repoError(err, "context not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_delete_context",
		mcp.WithDescription("Delete a context document by id."),
		mcp.WithString("id", mcp.Required(), mcp.Description("context id (ctx_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		if err := repo.Delete(ctx, id); err != nil {
			return repoError(err, "context not found")
		}
		return jsonResult(map[string]any{"id": id, "deleted": true})
	})
}
