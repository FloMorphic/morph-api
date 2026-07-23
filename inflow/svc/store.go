package svc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	inflowModels "github.com/Inflowenger/inflow-fusion/models"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
)

// storeRequest is the payload delivered to a store svc subject: the store the
// node references plus the action-specific fields the compiler carried. A doc
// store uses `query` (a read's SQL) and `input` (a write's object); a vector
// store uses `text` (the text to embed for an index or a search) and `topK`
// (how many neighbours a search returns). A vector index/search may also carry a
// `partition` — a per-record namespace/tag an index stamps on the record and a
// search restricts its top-k to.
type storeRequest struct {
	Action    string `json:"action"`
	StoreID   string `json:"storeId"`
	Query     string `json:"query"`
	Input     any    `json:"input"`
	Scope     string `json:"scope"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	TopK      int    `json:"topK"`
	Partition string `json:"partition"`
}

// HandleDocStore serves svc.store.doc.{read,write}. It resolves the referenced
// document store and dispatches: a read runs a validated, read-only SQL query;
// a write stores the request's JSON object as a document. A rejected or failing
// request returns an error so the run fails visibly rather than looking like an
// empty success.
func HandleDocStore(store repository.Store, header nats.Header, data []byte) ([]byte, error) {
	// The store fields (storeId/query/input/…) travel in the envelope's `op`
	// payload; the write document comes from the live scoped `Data`.
	body := parseRequest(data)
	req := decodeOp[storeRequest](body.OperationData)

	action := strings.ToLower(strings.TrimSpace(actionFromSubject(header, req.Action)))
	if strings.TrimSpace(req.StoreID) == "" {
		return nil, fmt.Errorf("doc store %s: request has no storeId", action)
	}
	// The store is resolved server-side; it — not the request — decides which
	// database/table is touched.
	rec, err := store.Memory().GetByID(context.Background(), req.StoreID)
	if err != nil {
		return nil, fmt.Errorf("doc store %s: store %q not found: %w", action, req.StoreID, err)
	}
	if rec.Type != models.MemoryDocument || rec.Document == nil {
		return nil, fmt.Errorf("doc store %s: store %q is not a document store", action, req.StoreID)
	}

	switch action {
	case "read":
		safe, err := models.ValidateReadSQL(req.Query)
		if err != nil {
			// A rejected query is a hard failure — never fall through to a read.
			return nil, fmt.Errorf("doc store read rejected: %w", err)
		}
		rows, err := store.Memory().RunReadQuery(context.Background(), rec, safe)
		if err != nil {
			return nil, fmt.Errorf("doc store read: %w", err)
		}
		resp, _ := sonic.Marshal(map[string]any{"status": "ok", "count": len(rows), "items": rows})
		return resp, nil
	case "write":
		// The value to store is the node-scoped object (e.g. `$.b`). A write
		// returns the id it saved under, which the runtime places back into that
		// scoped object under the node's key; on a re-run that id rides in as
		// `scope[key]`. So a re-run of the same node updates the row it created
		// before instead of inserting a duplicate — this is an upsert keyed on the
		// prior run's returned id.
		doc := documentPayload(req, body.Data)
		key := firstNonEmpty(req.Key, nodeKey(body.Node))
		// Persist the user's data only: drop the node's own result wrapper the
		// runtime merged back under `key`, so successive updates don't accrete our
		// bookkeeping into the stored document.
		stored := storableDoc(doc, key)
		if prevID := priorDocID(key, doc); prevID != "" {
			err := store.Memory().UpdateDocument(context.Background(), rec, prevID, stored)
			switch {
			case err == nil:
				resp, _ := sonic.Marshal(map[string]any{"status": "ok", "id": prevID, "op": "update"})
				return resp, nil
			case !errors.Is(err, repository.ErrNotFound):
				return nil, fmt.Errorf("doc store write (update %q): %w", prevID, err)
			}
			// ErrNotFound: the carried id no longer resolves to a row — fall
			// through and insert a fresh document.
		}
		id, err := store.Memory().WriteDocument(context.Background(), rec, stored)
		if err != nil {
			return nil, fmt.Errorf("doc store write: %w", err)
		}
		resp, _ := sonic.Marshal(map[string]any{"status": "ok", "id": id, "op": "insert"})
		return resp, nil
	default:
		return nil, fmt.Errorf("doc store: unsupported action %q", action)
	}
}

// HandleVecStore serves svc.store.vec.{write,index,read,search}. It resolves the
// referenced vector store and, using the embedding provider/model/token captured
// once when the store was created, either embeds-and-indexes the request's text
// (write/index) or embeds the query text and runs a similarity search
// (read/search). A rejected or failing request returns an error so the run fails
// visibly rather than looking like an empty success.
func HandleVecStore(store repository.Store, header nats.Header, data []byte) ([]byte, error) {
	// The store fields travel in the envelope's `op` payload; the text/object to
	// index can also come from the live scoped `Data`.
	body := parseRequest(data)
	req := decodeOp[storeRequest](body.OperationData)

	action := strings.ToLower(strings.TrimSpace(actionFromSubject(header, req.Action)))
	if strings.TrimSpace(req.StoreID) == "" {
		return nil, fmt.Errorf("vector store %s: request has no storeId", action)
	}
	rec, err := store.Memory().GetByID(context.Background(), req.StoreID)
	if err != nil {
		return nil, fmt.Errorf("vector store %s: store %q not found: %w", action, req.StoreID, err)
	}
	if rec.Type != models.MemoryVector || rec.Vector == nil {
		return nil, fmt.Errorf("vector store %s: store %q is not a vector store", action, req.StoreID)
	}

	ctx := context.Background()
	switch action {
	case "write", "index", "add", "upsert":
		// The text to embed: an explicit `text`, else a string `input`, else a
		// `text`/`content` field on the live scoped data. The metadata stored
		// alongside is the input/scoped object, so a search can return the origin.
		scoped := scopedDataMap(body.Data)
		text := vecIndexText(req, scoped)
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("vector store %s: no text to embed (set `text`, a string `input`, or a text/content field)", action)
		}
		vector, err := embedOne(ctx, rec.Vector, text)
		if err != nil {
			return nil, fmt.Errorf("vector store %s: %w", action, err)
		}
		partition := vecPartition(req, scoped)
		id, err := store.Memory().IndexVector(ctx, rec, text, vector, vecMetadata(req, scoped), partition)
		if err != nil {
			return nil, fmt.Errorf("vector store %s: %w", action, err)
		}
		resp, _ := sonic.Marshal(map[string]any{"status": "ok", "id": id, "partition": partition})
		return resp, nil
	case "read", "search", "query", "similar":
		query := firstNonEmpty(req.Text, req.Query)
		if strings.TrimSpace(query) == "" {
			return nil, fmt.Errorf("vector store %s: no query text (set `text` or `query`)", action)
		}
		vector, err := embedOne(ctx, rec.Vector, query)
		if err != nil {
			return nil, fmt.Errorf("vector store %s: %w", action, err)
		}
		matches, err := store.Memory().SearchVectors(ctx, rec, vector, req.TopK, vecPartition(req, scopedDataMap(body.Data)))
		if err != nil {
			return nil, fmt.Errorf("vector store %s: %w", action, err)
		}
		resp, _ := sonic.Marshal(map[string]any{"status": "ok", "count": len(matches), "items": matches})
		return resp, nil
	default:
		return nil, fmt.Errorf("vector store: unsupported action %q", action)
	}
}

// vecIndexText picks the text an index request should embed: an explicit `text`,
// then a string `input`, then a `text`/`content` string field on the live scoped
// data. Returns "" when none is present so the caller can reject the request.
func vecIndexText(req storeRequest, scoped map[string]any) string {
	if s := strings.TrimSpace(req.Text); s != "" {
		return req.Text
	}
	if s, ok := req.Input.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	for _, k := range []string{"text", "content"} {
		if s, ok := scoped[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// vecPartition resolves the partition/tag key for an index or search: an
// explicit `partition` on the node payload, else a `partition`/`namespace`/`tag`
// string on the live scoped data. Returns "" (un-partitioned / unfiltered) when
// none is present.
func vecPartition(req storeRequest, scoped map[string]any) string {
	if s := strings.TrimSpace(req.Partition); s != "" {
		return s
	}
	for _, k := range []string{"partition", "namespace", "tag"} {
		if s, ok := scoped[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// vecMetadata is the object stored alongside an indexed vector so a later search
// can return the original record: the node's `input` object when it is one,
// otherwise the flow's live scoped data.
func vecMetadata(req storeRequest, scoped map[string]any) map[string]any {
	if obj, ok := req.Input.(map[string]any); ok && len(obj) > 0 {
		return obj
	}
	return scoped
}

// nodeKey returns the compiled node's key (where the runtime places this node's
// result in the scoped data), or "" when the node isn't carried on the envelope.
func nodeKey(node *inflowModels.Node) string {
	if node == nil {
		return ""
	}
	return node.Key
}

// priorDocID recovers the id a previous run of this write node saved under, so a
// re-run updates that row instead of inserting a duplicate. A write returns its
// id in the node result, which the runtime merges back into the scoped object
// under the node's `key`; on the next run that wrapper arrives as `doc[key]` —
// either a bare id string or the result object carrying `id`/`_id`. Returns ""
// when the object was never saved (first run) or no key is configured.
func priorDocID(key string, doc map[string]any) string {
	key = strings.TrimSpace(key)
	if key == "" || doc == nil {
		return ""
	}
	switch v := doc[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, idKey := range []string{"id", "_id"} {
			if s, ok := v[idKey].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// storableDoc returns the document to persist with the node's own result wrapper
// removed: `doc[key]` is where the runtime merged this node's previous result
// (the {status,id} object), which is bookkeeping, not the user's data. Dropping
// it keeps stored documents to the scoped payload and stops updates from nesting
// prior results. The input map is never mutated; when there is nothing to strip
// the original is returned unchanged.
func storableDoc(doc map[string]any, key string) map[string]any {
	key = strings.TrimSpace(key)
	if key == "" || doc == nil {
		return doc
	}
	if _, ok := doc[key]; !ok {
		return doc
	}
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		if k == key {
			continue
		}
		out[k] = v
	}
	return out
}

// documentPayload derives the JSON object a write should store. When the node's
// `input` (from `op`) is already a concrete object, that object is the document;
// otherwise the flow's live scoped `Data` is stored, so a runtime value is never
// silently dropped.
func documentPayload(req storeRequest, scoped any) map[string]any {
	if obj, ok := req.Input.(map[string]any); ok && len(obj) > 0 {
		return obj
	}
	if obj := scopedDataMap(scoped); obj != nil {
		return obj
	}
	return map[string]any{}
}
