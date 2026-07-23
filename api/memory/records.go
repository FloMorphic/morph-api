package memoryControllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/inflow/svc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/gofiber/fiber/v3"
)

// This file adds the record-level REST surface over a memory store's data — the
// endpoints the web app's data browser uses. The store definitions live in
// crud.go; here we read and write the rows a store holds:
//
//   Document store (fixed id/data/timestamps table):
//     GET    /memory/:id/records        list rows (paginated)
//     POST   /memory/:id/records        insert a JSON document
//     PUT    /memory/:id/records/:rid   replace a document
//     DELETE /memory/:id/records/:rid   remove a document
//
//   Vector store (sqlite-vec index):
//     POST   /memory/:id/search         embed `text` and return nearest matches
//     POST   /memory/:id/vectors        embed `text` and index it with metadata
//
// The record primitives already exist on the repository/svc layer (they back the
// flow-runtime store nodes); these handlers just expose them over HTTP with the
// same server-side store resolution — the client never names a table or writes
// raw SQL.

// maxRecordLimit caps how many document rows a single list call returns.
const maxRecordLimit = 200

// documentRecord is the shape the web app's table/JSON views consume: the row
// id, its parsed JSON document, and the store's timestamps.
type documentRecord struct {
	ID        string         `json:"id"`
	Data      map[string]any `json:"data"`
	CreatedAt int64          `json:"createdAt"`
	UpdatedAt int64          `json:"updatedAt"`
}

// vectorSearchRequest is the body of POST /memory/:id/search. An optional
// `partition` restricts the search to records stored under that partition/tag.
type vectorSearchRequest struct {
	Text      string `json:"text"`
	TopK      int    `json:"topK"`
	Partition string `json:"partition"`
}

// vectorIndexRequest is the body of POST /memory/:id/vectors: the text to embed
// plus optional key/value metadata stored alongside so a search can return it,
// and an optional `partition` (a namespace/tag) a search can later filter on.
type vectorIndexRequest struct {
	Text      string         `json:"text"`
	Metadata  map[string]any `json:"metadata"`
	Partition string         `json:"partition"`
}

// docStore resolves :id and asserts it is a document store, writing the failure
// response and returning ok=false when it is not.
func (ctl *controller) docStore(c fiber.Ctx) (*models.MemoryStore, bool) {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		_ = etc.FailFromRepo(c, err, "memory store not found")
		return nil, false
	}
	if rec.Type != models.MemoryDocument || rec.Document == nil {
		_ = etc.Fail(c, fiber.StatusBadRequest, "store is not a document store")
		return nil, false
	}
	return rec, true
}

// vecStore resolves :id and asserts it is a vector store.
func (ctl *controller) vecStore(c fiber.Ctx) (*models.MemoryStore, bool) {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		_ = etc.FailFromRepo(c, err, "memory store not found")
		return nil, false
	}
	if rec.Type != models.MemoryVector || rec.Vector == nil {
		_ = etc.Fail(c, fiber.StatusBadRequest, "store is not a vector store")
		return nil, false
	}
	return rec, true
}

// listRecords handles GET /memory/:id/records — newest-first rows of a document
// store with limit/offset paging. The query is server-built over the validated
// table name (never client SQL), then run through the read-only path.
func (ctl *controller) listRecords(c fiber.Ctx) error {
	rec, ok := ctl.docStore(c)
	if !ok {
		return nil
	}
	limit := clampInt(queryInt(c, "limit", 50), 1, maxRecordLimit)
	offset := clampInt(queryInt(c, "offset", 0), 0, 1<<31)

	table := rec.Document.Table
	if !models.IsSafeIdentifier(table) {
		return etc.Fail(c, fiber.StatusBadRequest, "store table is not a valid identifier")
	}
	query := fmt.Sprintf(
		"SELECT id, data, created_at, updated_at FROM %s ORDER BY updated_at DESC LIMIT %d OFFSET %d",
		table, limit, offset,
	)
	rows, err := ctl.repo.RunReadQuery(c.Context(), rec, query)
	if err != nil {
		return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
	}
	items := make([]documentRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, rowToRecord(row))
	}
	return etc.OK(c, fiber.Map{"count": len(items), "items": items})
}

// createRecord handles POST /memory/:id/records — the body is the JSON document
// to store. Returns the generated id.
func (ctl *controller) createRecord(c fiber.Ctx) error {
	rec, ok := ctl.docStore(c)
	if !ok {
		return nil
	}
	doc, err := bindDocument(c)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	id, err := ctl.repo.WriteDocument(c.Context(), rec, doc)
	if err != nil {
		return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
	}
	return etc.OK(c, fiber.Map{"id": id})
}

// updateRecord handles PUT /memory/:id/records/:rid — replaces the document.
func (ctl *controller) updateRecord(c fiber.Ctx) error {
	rec, ok := ctl.docStore(c)
	if !ok {
		return nil
	}
	doc, err := bindDocument(c)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	if err := ctl.repo.UpdateDocument(c.Context(), rec, c.Params("rid"), doc); err != nil {
		return etc.FailFromRepo(c, err, "record not found")
	}
	return etc.OK(c, fiber.Map{"id": c.Params("rid")})
}

// deleteRecord handles DELETE /memory/:id/records/:rid.
func (ctl *controller) deleteRecord(c fiber.Ctx) error {
	rec, ok := ctl.docStore(c)
	if !ok {
		return nil
	}
	if err := ctl.repo.DeleteDocument(c.Context(), rec, c.Params("rid")); err != nil {
		return etc.FailFromRepo(c, err, "record not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": c.Params("rid")}, nil)
}

// searchVectors handles POST /memory/:id/search — embeds the query text with the
// store's captured embedding config and returns the nearest matches.
func (ctl *controller) searchVectors(c fiber.Ctx) error {
	rec, ok := ctl.vecStore(c)
	if !ok {
		return nil
	}
	var req vectorSearchRequest
	if err := c.Bind().Body(&req); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid search payload")
	}
	if strings.TrimSpace(req.Text) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "search text is required")
	}
	vector, err := svc.EmbedOne(context.Background(), rec.Vector, req.Text)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	matches, err := ctl.repo.SearchVectors(c.Context(), rec, vector, req.TopK, strings.TrimSpace(req.Partition))
	if err != nil {
		return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
	}
	return etc.OK(c, fiber.Map{"count": len(matches), "items": matches})
}

// indexVector handles POST /memory/:id/vectors — embeds the text and stores the
// resulting vector with its source content and metadata.
func (ctl *controller) indexVector(c fiber.Ctx) error {
	rec, ok := ctl.vecStore(c)
	if !ok {
		return nil
	}
	var req vectorIndexRequest
	if err := c.Bind().Body(&req); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid index payload")
	}
	if strings.TrimSpace(req.Text) == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "text to embed is required")
	}
	vector, err := svc.EmbedOne(context.Background(), rec.Vector, req.Text)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	id, err := ctl.repo.IndexVector(c.Context(), rec, req.Text, vector, req.Metadata, strings.TrimSpace(req.Partition))
	if err != nil {
		return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
	}
	return etc.OK(c, fiber.Map{"id": id})
}

// rowToRecord turns a RunReadQuery row (id/data/created_at/updated_at) into the
// documentRecord the web app consumes, parsing the stored JSON `data` blob.
func rowToRecord(row map[string]any) documentRecord {
	out := documentRecord{ID: toString(row["id"]), Data: map[string]any{}}
	if raw := toString(row["data"]); raw != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			out.Data = parsed
		}
	}
	out.CreatedAt = toInt64(row["created_at"])
	out.UpdatedAt = toInt64(row["updated_at"])
	return out
}

// bindDocument reads the request body as a single JSON object (the document).
func bindDocument(c fiber.Ctx) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(c.Body(), &doc); err != nil {
		return nil, fmt.Errorf("body must be a JSON object")
	}
	if doc == nil {
		return nil, fmt.Errorf("body must be a JSON object")
	}
	return doc, nil
}

func queryInt(c fiber.Ctx, key string, def int) int {
	if v := strings.TrimSpace(c.Query(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}
