package svc

import (
	"context"
	"reflect"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
)

// docMemory is a MemoryRepository stub that records the write path a doc-store
// request took (insert vs update) and the document it was handed.
type docMemory struct {
	repository.MemoryRepository
	rec *models.MemoryStore

	updatedID  string
	updatedDoc map[string]any
	updateErr  error

	insertedDoc map[string]any
}

func (m *docMemory) GetByID(context.Context, string) (*models.MemoryStore, error) {
	return m.rec, nil
}

func (m *docMemory) UpdateDocument(_ context.Context, _ *models.MemoryStore, id string, doc map[string]any) error {
	m.updatedID, m.updatedDoc = id, doc
	return m.updateErr
}

func (m *docMemory) WriteDocument(_ context.Context, _ *models.MemoryStore, doc map[string]any) (string, error) {
	m.insertedDoc = doc
	return "doc_new", nil
}

// docStore is a repository.Store exposing only Memory(); the doc handler touches
// nothing else.
type docStore struct {
	repository.Store
	mem *docMemory
}

func (s *docStore) Memory() repository.MemoryRepository { return s.mem }

func newDocStore(updateErr error) *docStore {
	return &docStore{mem: &docMemory{
		rec: &models.MemoryStore{
			ID:       "mem_1",
			Type:     models.MemoryDocument,
			Document: &models.DocumentMemoryConfig{},
		},
		updateErr: updateErr,
	}}
}

var writeHeader = nats.Header{"recv_subject": []string{"svc.store.doc.write"}}

// envelope builds the ExtSvcRequestBody inflow-fusion delivers: the live scoped
// data, the compile-time `op` payload, and the compiled node.
func envelope(scoped map[string]any, nodeKey string) []byte {
	b, _ := sonic.Marshal(map[string]any{
		"data": scoped,
		"op":   map[string]any{"action": "write", "storeId": "mem_1"},
		"node": map[string]any{"key": nodeKey, "scope": "$.js_result"},
	})
	return b
}

// The scoped data of a re-run carries this node's previous result under the
// node's key — the shape a real run produces:
//
//	"js_result": {"docResult": {"id": …, "op": "insert"}, "telemetry": …, "x": 10}
//
// The handler must recover that id from `Data[key]` and update the row instead
// of inserting a second one, and must not store its own result wrapper.
func TestDocWriteUpdatesOnPriorInsertID(t *testing.T) {
	st := newDocStore(nil)
	scoped := map[string]any{
		"docResult": map[string]any{"id": "doc_mry2bhbo49gt5wk3", "op": "insert", "status": "ok"},
		"telemetry": map[string]any{"op": "mov"},
		"x":         10,
	}

	raw, err := HandleDocStore(st, writeHeader, envelope(scoped, "docResult"))
	if err != nil {
		t.Fatalf("HandleDocStore: %v", err)
	}

	var resp map[string]any
	if err := sonic.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["op"] != "update" || resp["id"] != "doc_mry2bhbo49gt5wk3" {
		t.Fatalf("response = %v, want update of doc_mry2bhbo49gt5wk3", resp)
	}
	if st.mem.insertedDoc != nil {
		t.Fatalf("inserted a duplicate document: %v", st.mem.insertedDoc)
	}
	if st.mem.updatedID != "doc_mry2bhbo49gt5wk3" {
		t.Fatalf("updated id = %q, want doc_mry2bhbo49gt5wk3", st.mem.updatedID)
	}
	want := map[string]any{"telemetry": map[string]any{"op": "mov"}, "x": float64(10)}
	if !reflect.DeepEqual(st.mem.updatedDoc, want) {
		t.Fatalf("stored document = %v, want %v (result wrapper stripped)", st.mem.updatedDoc, want)
	}
}

// A node whose `input` names a concrete object stores that object — but its
// prior id lives on the surrounding scope, not inside the model, so the write
// must still resolve to an update.
func TestDocWriteFindsPriorIDWhenInputIsAConcreteObject(t *testing.T) {
	st := newDocStore(nil)
	raw, _ := sonic.Marshal(map[string]any{
		"data": map[string]any{
			"docResult": map[string]any{"id": "doc_mry2bhbo49gt5wk3", "op": "insert", "status": "ok"},
			"x":         10,
		},
		"op": map[string]any{
			"action": "write", "storeId": "mem_1",
			"input": map[string]any{"name": "ada", "role": "admin"},
		},
		"node": map[string]any{"key": "docResult", "scope": "$.js_result"},
	})

	resp := map[string]any{}
	out, err := HandleDocStore(st, writeHeader, raw)
	if err != nil {
		t.Fatalf("HandleDocStore: %v", err)
	}
	_ = sonic.Unmarshal(out, &resp)
	if resp["op"] != "update" || resp["id"] != "doc_mry2bhbo49gt5wk3" {
		t.Fatalf("response = %v, want update of doc_mry2bhbo49gt5wk3", resp)
	}
	want := map[string]any{"name": "ada", "role": "admin"}
	if !reflect.DeepEqual(st.mem.updatedDoc, want) {
		t.Fatalf("stored document = %v, want the input object %v", st.mem.updatedDoc, want)
	}
}

// The executed node on the envelope decides key/scope: a stale compile-time
// `key` in `op` must not shadow it.
func TestDocWritePrefersNodeKeyOverOpKey(t *testing.T) {
	st := newDocStore(nil)
	raw, _ := sonic.Marshal(map[string]any{
		"data": map[string]any{
			"docResult": map[string]any{"id": "doc_mry2bhbo49gt5wk3", "op": "insert", "status": "ok"},
			"x":         10,
		},
		"op":   map[string]any{"action": "write", "storeId": "mem_1", "key": "staleKey"},
		"node": map[string]any{"key": "docResult", "scope": "$.js_result"},
	})

	out, err := HandleDocStore(st, writeHeader, raw)
	if err != nil {
		t.Fatalf("HandleDocStore: %v", err)
	}
	resp := map[string]any{}
	_ = sonic.Unmarshal(out, &resp)
	if resp["op"] != "update" || resp["id"] != "doc_mry2bhbo49gt5wk3" {
		t.Fatalf("response = %v, want update of doc_mry2bhbo49gt5wk3", resp)
	}
}

// First run: the scoped data has no wrapper under the node's key, so the write
// inserts and reports the id it saved under.
func TestDocWriteInsertsOnFirstRun(t *testing.T) {
	st := newDocStore(nil)
	scoped := map[string]any{"telemetry": map[string]any{"op": "mov"}, "x": 10}

	raw, err := HandleDocStore(st, writeHeader, envelope(scoped, "docResult"))
	if err != nil {
		t.Fatalf("HandleDocStore: %v", err)
	}
	var resp map[string]any
	_ = sonic.Unmarshal(raw, &resp)
	if resp["op"] != "insert" || resp["id"] != "doc_new" {
		t.Fatalf("response = %v, want insert of doc_new", resp)
	}
	if st.mem.updatedID != "" {
		t.Fatalf("attempted an update with no prior id: %q", st.mem.updatedID)
	}
}

// A carried id whose row is gone (store cleared between runs) must not fail the
// run — the write falls through to a fresh insert.
func TestDocWriteFallsBackToInsertWhenPriorRowIsGone(t *testing.T) {
	st := newDocStore(repository.ErrNotFound)
	scoped := map[string]any{
		"docResult": map[string]any{"id": "doc_stale", "op": "insert", "status": "ok"},
		"x":         10,
	}

	raw, err := HandleDocStore(st, writeHeader, envelope(scoped, "docResult"))
	if err != nil {
		t.Fatalf("HandleDocStore: %v", err)
	}
	var resp map[string]any
	_ = sonic.Unmarshal(raw, &resp)
	if resp["op"] != "insert" || resp["id"] != "doc_new" {
		t.Fatalf("response = %v, want insert of doc_new", resp)
	}
	if _, carried := st.mem.insertedDoc["docResult"]; carried {
		t.Fatalf("inserted document kept the result wrapper: %v", st.mem.insertedDoc)
	}
}
