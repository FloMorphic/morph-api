package mcpserver

import (
	"context"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerSettingsTools exposes the /settings surface — node settings profiles,
// the reusable named config bound to a node's kind/plugin identity (nodeUniqId),
// e.g. a plugin's access token or a HITL bot's LLM provider. Full CRUD, with the
// `node` filter the node drawer uses.
func registerSettingsTools(s *server.MCPServer, store repository.Store) {
	repo := store.NodeSettings()

	s.AddTool(mcp.NewTool("flo_list_node_settings",
		withOpts(pageArgs(),
			mcp.WithDescription("List node settings profiles, newest first. Filter to one node's profiles with `node` (its nodeUniqId)."),
			mcp.WithString("node", mcp.Description("nodeUniqId to scope to one node's profiles")),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p := listParams(req)
		p.NodeUniqID = strings.TrimSpace(req.GetString("node", ""))
		items, total, err := repo.List(ctx, p)
		if err != nil {
			return repoError(err, "settings not found")
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_node_setting",
		mcp.WithDescription("Get one node settings profile by id, including its settings key/value object."),
		mcp.WithString("id", mcp.Required(), mcp.Description("settings profile id (nset_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "settings profile not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_upsert_node_setting",
		mcp.WithDescription("Create (empty id) or update a node settings profile. `settings` is the free-form key/value config for the node (may hold provider tokens, endpoints, …)."),
		mcp.WithString("id", mcp.Description("profile id; empty creates a new one")),
		mcp.WithString("nodeUniqId", mcp.Required(), mcp.Description("the node kind / plugin identity the profile is bound to")),
		mcp.WithString("nodeType", mcp.Description("the node's kind (e.g. llm, plugin, http)")),
		mcp.WithString("title", mcp.Description("a name for the profile")),
		mcp.WithObject("settings", mcp.Required(), mcp.Description("the settings key/value object")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		nodeUniqID, err := req.RequireString("nodeUniqId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("nodeUniqId is required", err), nil
		}
		settings, err := argObject(req, "settings")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("invalid settings", err), nil
		}
		rec := models.NodeSetting{
			ID:         req.GetString("id", ""),
			NodeUniqID: nodeUniqID,
			NodeType:   req.GetString("nodeType", ""),
			Title:      req.GetString("title", ""),
			Settings:   settings,
		}
		if err := repo.Upsert(ctx, &rec); err != nil {
			return repoError(err, "settings profile not found")
		}
		return jsonResult(rec)
	})

	s.AddTool(mcp.NewTool("flo_delete_node_setting",
		mcp.WithDescription("Delete a node settings profile by id."),
		mcp.WithString("id", mcp.Required(), mcp.Description("settings profile id (nset_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		if err := repo.Delete(ctx, id); err != nil {
			return repoError(err, "settings profile not found")
		}
		return jsonResult(map[string]any{"id": id, "deleted": true})
	})
}
