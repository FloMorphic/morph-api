package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/inflow/svc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerMemoryTools exposes the /memory surface: the store catalog, vector
// search / index (embedding with the store's captured config, exactly as
// api/memory/records.go does), and document-store reads (read-only SQL through
// the ValidateReadSQL guard) and writes.
func registerMemoryTools(s *server.MCPServer, store repository.Store) {
	repo := store.Memory()

	// getStore resolves an id and asserts the expected type, returning a tool
	// error result when it is missing or the wrong kind.
	getStore := func(ctx context.Context, id string, want models.MemoryType) (*models.MemoryStore, *mcp.CallToolResult) {
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			res, _ := repoError(err, "memory store not found")
			return nil, res
		}
		switch want {
		case models.MemoryVector:
			if rec.Type != models.MemoryVector || rec.Vector == nil {
				return nil, mcp.NewToolResultError("store is not a vector store")
			}
		case models.MemoryDocument:
			if rec.Type != models.MemoryDocument || rec.Document == nil {
				return nil, mcp.NewToolResultError("store is not a document store")
			}
		}
		return rec, nil
	}

	s.AddTool(mcp.NewTool("flo_list_memory_stores",
		mcp.WithDescription("List all memory stores (vector and document), newest first."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, err := repo.List(ctx)
		if err != nil {
			return repoError(err, "memory stores not found")
		}
		return jsonResult(map[string]any{"list": items, "total": len(items)})
	})

	s.AddTool(mcp.NewTool("flo_get_memory_store",
		mcp.WithDescription("Get one memory store by id — its type and vector/document config."),
		mcp.WithString("id", mcp.Required(), mcp.Description("memory store id (mem_…)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "memory store not found")
		}
		return jsonResult(rec)
	})

	// --- vector stores ------------------------------------------------------

	s.AddTool(mcp.NewTool("flo_search_vectors",
		mcp.WithDescription("Semantic search over a vector store: embeds `text` with the store's captured embedding config and returns the nearest matches (content, metadata, distance)."),
		mcp.WithString("storeId", mcp.Required(), mcp.Description("vector store id (mem_…)")),
		mcp.WithString("text", mcp.Required(), mcp.Description("query text to embed and search by")),
		mcp.WithNumber("topK", mcp.Description("number of matches to return")),
		mcp.WithString("partition", mcp.Description("restrict the search to this partition/tag")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("storeId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("storeId is required", err), nil
		}
		text, err := req.RequireString("text")
		if err != nil || strings.TrimSpace(text) == "" {
			return mcp.NewToolResultError("search text is required"), nil
		}
		rec, bad := getStore(ctx, id, models.MemoryVector)
		if bad != nil {
			return bad, nil
		}
		vector, err := svc.EmbedOne(ctx, rec.Vector, text)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("embedding failed", err), nil
		}
		matches, err := repo.SearchVectors(ctx, rec, vector, req.GetInt("topK", 0), strings.TrimSpace(req.GetString("partition", "")))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("search failed", err), nil
		}
		return jsonResult(map[string]any{"count": len(matches), "items": matches})
	})

	s.AddTool(mcp.NewTool("flo_index_vector",
		mcp.WithDescription("Embed `text` with the store's config and index it, with optional metadata and partition. Returns the stored id."),
		mcp.WithString("storeId", mcp.Required(), mcp.Description("vector store id (mem_…)")),
		mcp.WithString("text", mcp.Required(), mcp.Description("text to embed and store")),
		mcp.WithObject("metadata", mcp.Description("optional key/value metadata stored alongside")),
		mcp.WithString("partition", mcp.Description("optional partition/tag a later search can filter on")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("storeId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("storeId is required", err), nil
		}
		text, err := req.RequireString("text")
		if err != nil || strings.TrimSpace(text) == "" {
			return mcp.NewToolResultError("text to embed is required"), nil
		}
		meta, err := argObject(req, "metadata")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("invalid metadata", err), nil
		}
		rec, bad := getStore(ctx, id, models.MemoryVector)
		if bad != nil {
			return bad, nil
		}
		vector, err := svc.EmbedOne(ctx, rec.Vector, text)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("embedding failed", err), nil
		}
		docID, err := repo.IndexVector(ctx, rec, text, vector, meta, strings.TrimSpace(req.GetString("partition", "")))
		if err != nil {
			return mcp.NewToolResultErrorFromErr("index failed", err), nil
		}
		return jsonResult(map[string]any{"id": docID})
	})

	// --- document stores ----------------------------------------------------

	s.AddTool(mcp.NewTool("flo_query_documents",
		mcp.WithDescription("Run a READ-ONLY SQL query over a document store. Only a single SELECT (or WITH…SELECT) is allowed; writes, DDL, PRAGMA, comments and multiple statements are rejected. An unbounded query is capped at 1000 rows."),
		mcp.WithString("storeId", mcp.Required(), mcp.Description("document store id (mem_…)")),
		mcp.WithString("sql", mcp.Required(), mcp.Description("a single SELECT; the store's backing table name is in its document config")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("storeId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("storeId is required", err), nil
		}
		sql, err := req.RequireString("sql")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("sql is required", err), nil
		}
		rec, bad := getStore(ctx, id, models.MemoryDocument)
		if bad != nil {
			return bad, nil
		}
		safe, err := validReadSQL(sql)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rows, err := repo.RunReadQuery(ctx, rec, safe)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("query failed", err), nil
		}
		return jsonResult(map[string]any{"count": len(rows), "rows": rows, "table": rec.Document.Table})
	})

	s.AddTool(mcp.NewTool("flo_list_documents",
		mcp.WithDescription("List rows of a document store, newest first, with limit/offset."),
		mcp.WithString("storeId", mcp.Required(), mcp.Description("document store id (mem_…)")),
		mcp.WithNumber("limit", mcp.Description("rows to return, 1-200 (default 50)")),
		mcp.WithNumber("offset", mcp.Description("rows to skip (default 0)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("storeId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("storeId is required", err), nil
		}
		rec, bad := getStore(ctx, id, models.MemoryDocument)
		if bad != nil {
			return bad, nil
		}
		table := rec.Document.Table
		if !models.IsSafeIdentifier(table) {
			return mcp.NewToolResultError("store table is not a valid identifier"), nil
		}
		limit := clamp(req.GetInt("limit", 50), 1, 200)
		offset := req.GetInt("offset", 0)
		if offset < 0 {
			offset = 0
		}
		// Server-built over the validated table name, then run through the same
		// read-only path — never client SQL (mirrors listRecords in records.go).
		query := fmt.Sprintf("SELECT id, data, created_at, updated_at FROM %s ORDER BY updated_at DESC LIMIT %d OFFSET %d", table, limit, offset)
		rows, err := repo.RunReadQuery(ctx, rec, query)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("query failed", err), nil
		}
		return jsonResult(map[string]any{"count": len(rows), "rows": rows})
	})

	s.AddTool(mcp.NewTool("flo_write_document",
		mcp.WithDescription("Store one JSON document in a document store. Returns the generated id."),
		mcp.WithString("storeId", mcp.Required(), mcp.Description("document store id (mem_…)")),
		mcp.WithObject("document", mcp.Required(), mcp.Description("the JSON document to store")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rec, doc, bad := resolveDocWrite(ctx, getStore, req)
		if bad != nil {
			return bad, nil
		}
		docID, err := repo.WriteDocument(ctx, rec, doc)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("write failed", err), nil
		}
		return jsonResult(map[string]any{"id": docID})
	})

	s.AddTool(mcp.NewTool("flo_update_document",
		mcp.WithDescription("Replace the JSON document stored under `docId` in a document store."),
		mcp.WithString("storeId", mcp.Required(), mcp.Description("document store id (mem_…)")),
		mcp.WithString("docId", mcp.Required(), mcp.Description("id of the row to replace")),
		mcp.WithObject("document", mcp.Required(), mcp.Description("the replacement JSON document")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docID, err := req.RequireString("docId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("docId is required", err), nil
		}
		rec, doc, bad := resolveDocWrite(ctx, getStore, req)
		if bad != nil {
			return bad, nil
		}
		if err := repo.UpdateDocument(ctx, rec, docID, doc); err != nil {
			return repoError(err, "document not found")
		}
		return jsonResult(map[string]any{"id": docID, "updated": true})
	})

	s.AddTool(mcp.NewTool("flo_delete_document",
		mcp.WithDescription("Delete the document with `docId` from a document store."),
		mcp.WithString("storeId", mcp.Required(), mcp.Description("document store id (mem_…)")),
		mcp.WithString("docId", mcp.Required(), mcp.Description("id of the row to delete")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("storeId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("storeId is required", err), nil
		}
		docID, err := req.RequireString("docId")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("docId is required", err), nil
		}
		rec, bad := getStore(ctx, id, models.MemoryDocument)
		if bad != nil {
			return bad, nil
		}
		if err := repo.DeleteDocument(ctx, rec, docID); err != nil {
			return repoError(err, "document not found")
		}
		return jsonResult(map[string]any{"id": docID, "deleted": true})
	})
}

// resolveDocWrite is the shared prologue of the document write/update tools: it
// resolves the store as a document store and reads the required `document`
// object. On failure it returns a ready tool-error result in the third slot.
func resolveDocWrite(
	ctx context.Context,
	getStore func(context.Context, string, models.MemoryType) (*models.MemoryStore, *mcp.CallToolResult),
	req mcp.CallToolRequest,
) (*models.MemoryStore, map[string]any, *mcp.CallToolResult) {
	id, err := req.RequireString("storeId")
	if err != nil {
		return nil, nil, mcp.NewToolResultError("storeId is required")
	}
	doc, err := argObject(req, "document")
	if err != nil {
		return nil, nil, mcp.NewToolResultErrorFromErr("invalid document", err)
	}
	if doc == nil {
		return nil, nil, mcp.NewToolResultError("document is required")
	}
	rec, bad := getStore(ctx, id, models.MemoryDocument)
	if bad != nil {
		return nil, nil, bad
	}
	return rec, doc, nil
}

// clamp bounds v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
