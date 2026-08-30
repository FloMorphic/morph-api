package svc

import (
	"reflect"
	"testing"

	inflowModels "github.com/Inflowenger/inflow-fusion/models"
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

// A vector-store write embeds the text chosen by vecIndexText. The `text`
// parameter (a {{$.path}} placeholder the engine resolves before the call, or
// literal text) is the intended input and must win; `input` — the metadata
// object for a vector store — must never be mistaken for the embed text; and
// legacy scopes (a text/content field, or a bare-string scope) still resolve.
func TestVecIndexText(t *testing.T) {
	cases := []struct {
		name    string
		req     storeRequest
		scoped  map[string]any
		rawData any
		want    string
	}{
		{
			name: "resolved text parameter wins",
			req:  storeRequest{Text: "embed me"},
			want: "embed me",
		},
		{
			name:    "text parameter beats scoped content",
			req:     storeRequest{Text: "from param"},
			scoped:  map[string]any{"content": "from scope"},
			rawData: map[string]any{"content": "from scope"},
			want:    "from param",
		},
		{
			// The old bug: `input` defaults to the JSONPath "$", which was
			// embedded literally. It is metadata, not text — must be ignored.
			name:    "string input is not the embed text",
			req:     storeRequest{Input: "$"},
			scoped:  map[string]any{"other": 1},
			rawData: map[string]any{"other": 1},
			want:    "",
		},
		{
			name:    "falls back to scoped content field",
			scoped:  map[string]any{"content": "hello world"},
			rawData: map[string]any{"content": "hello world"},
			want:    "hello world",
		},
		{
			name:    "falls back to scoped text field",
			scoped:  map[string]any{"text": "some text"},
			rawData: map[string]any{"text": "some text"},
			want:    "some text",
		},
		{
			// scope `$.content.text` resolves to a bare string, delivered as
			// Data (not a map), so scopedDataMap is nil.
			name:    "bare-string scope resolves as text",
			rawData: "just a string",
			want:    "just a string",
		},
		{
			name:    "blank everywhere yields empty",
			req:     storeRequest{Text: "  ", Input: "  "},
			scoped:  map[string]any{"content": "   "},
			rawData: "   ",
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vecIndexText(tc.req, tc.scoped, tc.rawData); got != tc.want {
				t.Fatalf("vecIndexText = %q, want %q", got, tc.want)
			}
		})
	}
}

// A workflow vector node carries its metadata rows as flattened `meta.<key>`
// entries on the resolved `op` payload (each value already substituted by the
// engine — a {{$.path}} placeholder arrives as its context value). opMetadata
// must reassemble exactly those, stripping the prefix and dropping blanks, and
// ignore the ordinary store fields sitting beside them.
func TestOpMetadata(t *testing.T) {
	body := inflowModels.ExtSvcRequestBody{OperationData: map[string]any{
		"action":     "write",
		"storeId":    "mem_1",
		"text":       "embed me",
		"meta.env":   "prod",      // resolved from {{$.env}}
		"meta.x":     float64(42), // exact-placeholder resolve kept the number
		"meta.blank": "  ",        // blank value dropped
		"meta.":      "orphan",    // empty key dropped
	}}
	got := opMetadata(body)
	want := map[string]any{"env": "prod", "x": float64(42)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("opMetadata = %v, want %v", got, want)
	}
}

func TestOpMetadataEmptyIsNil(t *testing.T) {
	body := inflowModels.ExtSvcRequestBody{OperationData: map[string]any{"action": "write", "storeId": "mem_1"}}
	if got := opMetadata(body); got != nil {
		t.Fatalf("opMetadata with no meta.* = %v, want nil (unfiltered)", got)
	}
}

// Explicit metadata rows win; without them the write falls back to the input
// object, then the scoped data, so older nodes keep working.
func TestVecWriteMetadata(t *testing.T) {
	scoped := map[string]any{"from": "scope"}
	t.Run("explicit meta rows win", func(t *testing.T) {
		body := inflowModels.ExtSvcRequestBody{OperationData: map[string]any{"meta.k": "v"}}
		got := vecWriteMetadata(body, storeRequest{Input: map[string]any{"from": "input"}}, scoped)
		if !reflect.DeepEqual(got, map[string]any{"k": "v"}) {
			t.Fatalf("got %v, want {k:v}", got)
		}
	})
	t.Run("falls back to input object", func(t *testing.T) {
		body := inflowModels.ExtSvcRequestBody{}
		got := vecWriteMetadata(body, storeRequest{Input: map[string]any{"from": "input"}}, scoped)
		if !reflect.DeepEqual(got, map[string]any{"from": "input"}) {
			t.Fatalf("got %v, want the input object", got)
		}
	})
	t.Run("falls back to scoped data", func(t *testing.T) {
		body := inflowModels.ExtSvcRequestBody{}
		got := vecWriteMetadata(body, storeRequest{}, scoped)
		if !reflect.DeepEqual(got, scoped) {
			t.Fatalf("got %v, want the scoped data", got)
		}
	})
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
