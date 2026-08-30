package sqlite

import (
	"context"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// TestVectorPartitioning covers the per-record partition/tag key: an index
// stamps it, and a search filters its top-k to a single partition while an
// unfiltered search still spans them all. The partition is also echoed back on
// each match.
func TestVectorPartitioning(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	mem := st.Memory()

	store := &models.MemoryStore{
		Name: "vec",
		Type: models.MemoryVector,
		Vector: &models.VectorMemoryConfig{
			Dimensions: 3,
			Metric:     models.MetricCosine,
		},
	}
	if err := mem.Create(ctx, store); err != nil {
		t.Fatalf("create vector store: %v", err)
	}

	index := func(partition, content string, v []float32) string {
		id, err := mem.IndexVector(ctx, store, content, v, map[string]any{"content": content}, partition)
		if err != nil {
			t.Fatalf("index (%s/%s): %v", partition, content, err)
		}
		return id
	}
	index("p1", "a", []float32{1, 0, 0})
	index("p1", "b", []float32{0.9, 0.1, 0})
	index("p2", "c", []float32{1, 0, 0})
	index("", "d", []float32{0, 1, 0})

	contents := func(ms []models.VectorMatch) map[string]bool {
		out := map[string]bool{}
		for _, m := range ms {
			out[m.Content] = true
		}
		return out
	}

	query := []float32{1, 0, 0}

	// Filtered to p1: only a and b, and each echoes its partition.
	p1, err := mem.SearchVectors(ctx, store, query, 10, "p1", 0, nil)
	if err != nil {
		t.Fatalf("search p1: %v", err)
	}
	if got := contents(p1); len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("search p1 = %v, want {a,b}", got)
	}
	for _, m := range p1 {
		if m.Partition != "p1" {
			t.Fatalf("match %q partition = %q, want p1", m.Content, m.Partition)
		}
	}

	// Filtered to p2: only c.
	p2, err := mem.SearchVectors(ctx, store, query, 10, "p2", 0, nil)
	if err != nil {
		t.Fatalf("search p2: %v", err)
	}
	if got := contents(p2); len(got) != 1 || !got["c"] {
		t.Fatalf("search p2 = %v, want {c}", got)
	}

	// Unfiltered: spans every partition.
	all, err := mem.SearchVectors(ctx, store, query, 10, "", 0, nil)
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if got := contents(all); len(got) != 4 {
		t.Fatalf("search all = %v, want 4 records", got)
	}
}

// TestVectorMetadataFilter covers the read-side metadata filter: a search keeps
// only records whose stored metadata contains every requested key/value pair,
// applied after the KNN so results stay nearest-first. The comparison tolerates
// JSON type drift (a stored number matched by its string form).
func TestVectorMetadataFilter(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	mem := st.Memory()

	store := &models.MemoryStore{
		Name:   "vec",
		Type:   models.MemoryVector,
		Vector: &models.VectorMemoryConfig{Dimensions: 3, Metric: models.MetricCosine},
	}
	if err := mem.Create(ctx, store); err != nil {
		t.Fatalf("create vector store: %v", err)
	}

	index := func(content string, meta map[string]any, v []float32) {
		if _, err := mem.IndexVector(ctx, store, content, v, meta, ""); err != nil {
			t.Fatalf("index %s: %v", content, err)
		}
	}
	// All near the same direction so ranking never excludes a record — the filter
	// alone decides what comes back.
	index("a", map[string]any{"env": "prod", "n": 1}, []float32{1, 0, 0})
	index("b", map[string]any{"env": "prod", "n": 2}, []float32{0.99, 0.01, 0})
	index("c", map[string]any{"env": "staging", "n": 3}, []float32{0.98, 0.02, 0})

	query := []float32{1, 0, 0}
	contents := func(ms []models.VectorMatch) map[string]bool {
		out := map[string]bool{}
		for _, m := range ms {
			out[m.Content] = true
		}
		return out
	}

	// Single-key filter: only prod records.
	prod, err := mem.SearchVectors(ctx, store, query, 10, "", 0, map[string]any{"env": "prod"})
	if err != nil {
		t.Fatalf("search env=prod: %v", err)
	}
	if got := contents(prod); len(got) != 2 || !got["a"] || !got["b"] {
		t.Fatalf("env=prod = %v, want {a,b}", got)
	}

	// Multi-key AND: env=prod AND n=2 (given as a string, matched against the
	// stored number) → only b.
	both, err := mem.SearchVectors(ctx, store, query, 10, "", 0, map[string]any{"env": "prod", "n": "2"})
	if err != nil {
		t.Fatalf("search env=prod,n=2: %v", err)
	}
	if got := contents(both); len(got) != 1 || !got["b"] {
		t.Fatalf("env=prod,n=2 = %v, want {b}", got)
	}

	// A filter no record satisfies returns nothing (not an error).
	none, err := mem.SearchVectors(ctx, store, query, 10, "", 0, map[string]any{"env": "nope"})
	if err != nil {
		t.Fatalf("search env=nope: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("env=nope = %v, want no matches", contents(none))
	}
}
