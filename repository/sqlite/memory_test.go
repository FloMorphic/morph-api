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
	p1, err := mem.SearchVectors(ctx, store, query, 10, "p1")
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
	p2, err := mem.SearchVectors(ctx, store, query, 10, "p2")
	if err != nil {
		t.Fatalf("search p2: %v", err)
	}
	if got := contents(p2); len(got) != 1 || !got["c"] {
		t.Fatalf("search p2 = %v, want {c}", got)
	}

	// Unfiltered: spans every partition.
	all, err := mem.SearchVectors(ctx, store, query, 10, "")
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if got := contents(all); len(got) != 4 {
		t.Fatalf("search all = %v, want 4 records", got)
	}
}
