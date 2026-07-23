package svc

import (
	"reflect"
	"testing"
)

func TestPriorDocID(t *testing.T) {
	cases := []struct {
		name string
		key  string
		doc  map[string]any
		want string
	}{
		{
			name: "first run has no wrapper",
			key:  "saved",
			doc:  map[string]any{"foo": 1},
			want: "",
		},
		{
			name: "result object carries id",
			key:  "saved",
			doc:  map[string]any{"foo": 1, "saved": map[string]any{"status": "ok", "id": "doc_abc"}},
			want: "doc_abc",
		},
		{
			name: "result object carries _id",
			key:  "saved",
			doc:  map[string]any{"saved": map[string]any{"_id": "doc_xyz"}},
			want: "doc_xyz",
		},
		{
			name: "bare string id under key",
			key:  "saved",
			doc:  map[string]any{"saved": "doc_bare"},
			want: "doc_bare",
		},
		{
			name: "blank id is treated as unsaved",
			key:  "saved",
			doc:  map[string]any{"saved": map[string]any{"id": "  "}},
			want: "",
		},
		{
			name: "no key configured",
			key:  "",
			doc:  map[string]any{"saved": map[string]any{"id": "doc_abc"}},
			want: "",
		},
		{
			name: "non-string id is ignored",
			key:  "saved",
			doc:  map[string]any{"saved": map[string]any{"id": 42}},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := priorDocID(tc.key, tc.doc); got != tc.want {
				t.Fatalf("priorDocID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStorableDoc(t *testing.T) {
	t.Run("strips the result wrapper and does not mutate the input", func(t *testing.T) {
		doc := map[string]any{"foo": 1, "bar": 2, "saved": map[string]any{"id": "doc_abc"}}
		got := storableDoc(doc, "saved")
		want := map[string]any{"foo": 1, "bar": 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("storableDoc = %v, want %v", got, want)
		}
		if _, ok := doc["saved"]; !ok {
			t.Fatalf("storableDoc mutated its input map")
		}
	})

	t.Run("returns the original when there is nothing to strip", func(t *testing.T) {
		doc := map[string]any{"foo": 1}
		if got := storableDoc(doc, "saved"); !reflect.DeepEqual(got, doc) {
			t.Fatalf("storableDoc = %v, want %v", got, doc)
		}
	})

	t.Run("no key configured returns the doc unchanged", func(t *testing.T) {
		doc := map[string]any{"foo": 1, "saved": "x"}
		if got := storableDoc(doc, ""); !reflect.DeepEqual(got, doc) {
			t.Fatalf("storableDoc = %v, want %v", got, doc)
		}
	})
}
