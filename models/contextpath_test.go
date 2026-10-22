package models

import (
	"reflect"
	"strings"
	"testing"
)

// The contract a seed rests on: it writes the one location its path names,
// creates what that path needs, and leaves the rest of the context document
// alone. Everything here is about what must NOT change.

func TestSetAtContextPathLeavesTheRestAlone(t *testing.T) {
	doc := map[string]any{
		"order":  map[string]any{"id": "A-1"},
		"_sched": map[string]any{"traverse": []any{"n1"}},
		"count":  float64(3),
	}
	if err := SetAtContextPath(doc, "$._entry", map[string]any{"threshold": float64(10)}); err != nil {
		t.Fatalf("set: %v", err)
	}
	entry, ok := doc["_entry"].(map[string]any)
	if !ok || entry["threshold"] != float64(10) {
		t.Fatalf("_entry = %#v", doc["_entry"])
	}
	// The run's own state and the data another node left are still there.
	if _, ok := doc["_sched"]; !ok {
		t.Error("the seed dropped the engine's _sched header")
	}
	if order, _ := doc["order"].(map[string]any); order["id"] != "A-1" {
		t.Errorf("order = %#v", doc["order"])
	}
	if doc["count"] != float64(3) {
		t.Errorf("count = %#v", doc["count"])
	}
}

func TestSetAtContextPathCreatesMissingObjects(t *testing.T) {
	doc := map[string]any{"keep": true}
	if err := SetAtContextPath(doc, "$.input.order.meta", map[string]any{"id": "A-1"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	input, _ := doc["input"].(map[string]any)
	order, _ := input["order"].(map[string]any)
	meta, _ := order["meta"].(map[string]any)
	if meta["id"] != "A-1" {
		t.Fatalf("doc = %#v", doc)
	}
	if doc["keep"] != true {
		t.Error("creating the path disturbed a sibling")
	}
}

func TestSetAtContextPathKeepsSiblingsUnderTheTarget(t *testing.T) {
	doc := map[string]any{"input": map[string]any{"order": map[string]any{"id": "A-1"}, "other": "untouched"}}
	if err := SetAtContextPath(doc, "$.input.order", map[string]any{"id": "B-2"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	input, _ := doc["input"].(map[string]any)
	if input["other"] != "untouched" {
		t.Errorf("sibling of the target was rewritten: %#v", input)
	}
	// The target member itself is the value's own slot: it is replaced, not merged.
	if order, _ := input["order"].(map[string]any); order["id"] != "B-2" || len(order) != 1 {
		t.Errorf("order = %#v", input["order"])
	}
}

func TestSetAtContextPathRootMerges(t *testing.T) {
	doc := map[string]any{"kept": 1, "orderId": "A-1"}
	if err := SetAtContextPath(doc, "$", map[string]any{"orderId": "B-2", "added": true}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if doc["kept"] != 1 {
		t.Error("a root seed replaced the document instead of merging into it")
	}
	if doc["orderId"] != "B-2" || doc["added"] != true {
		t.Errorf("doc = %#v", doc)
	}
}

func TestSetAtContextPathRefusesToClobber(t *testing.T) {
	doc := map[string]any{"a": float64(42)}
	err := SetAtContextPath(doc, "$.a.b", map[string]any{"x": 1})
	if err == nil {
		t.Fatal("writing through a number was allowed")
	}
	if !strings.Contains(err.Error(), "$.a") {
		t.Errorf("the error does not name the segment in the way: %v", err)
	}
	if doc["a"] != float64(42) {
		t.Error("the refused write still changed the document")
	}
}

func TestParseContextPath(t *testing.T) {
	ok := map[string][]string{
		"$":             nil,
		"":              nil,
		"$._entry":      {"_entry"},
		"_entry":        {"_entry"},
		"$.input.order": {"input", "order"},
		"$['order id']": {"order id"},
		`$.a["b c"].d`:  {"a", "b c", "d"},
	}
	for path, want := range ok {
		got, err := ParseContextPath(path)
		if err != nil {
			t.Errorf("%q: %v", path, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %v, want %v", path, got, want)
		}
	}
	// Anything that selects zero or many locations is refused: a seed has to
	// know exactly where it writes.
	for _, path := range []string{"$.items[*]", "$.items[0]", "$..id", "$.items[?(@.ok)]", "$.a.", "$['a"} {
		if _, err := ParseContextPath(path); err == nil {
			t.Errorf("%q was accepted as a write target", path)
		}
	}
}

func TestValueAtContextPath(t *testing.T) {
	doc := map[string]any{"_entry": map[string]any{"threshold": float64(10)}, "n": float64(1)}
	if v, ok := ValueAtContextPath(doc, "$._entry"); !ok || v.(map[string]any)["threshold"] != float64(10) {
		t.Errorf("read = %#v %v", v, ok)
	}
	if _, ok := ValueAtContextPath(doc, "$.missing"); ok {
		t.Error("an absent path read as present")
	}
	// A path that runs through a scalar is absent, not an error — a reader only
	// wants to know whether there is something to show.
	if _, ok := ValueAtContextPath(doc, "$.n.deeper"); ok {
		t.Error("a path through a number read as present")
	}
}

func TestIsSchemaDocument(t *testing.T) {
	schemas := []map[string]any{
		{"$schema": "https://json-schema.org/draft/2020-12/schema"},
		{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
	}
	for _, doc := range schemas {
		if !IsSchemaDocument(doc) {
			t.Errorf("%#v read as plain values", doc)
		}
	}
	// A person's own data is not a contract, however schema-ish a key looks.
	plain := []map[string]any{
		{"type": "order", "threshold": 10},
		{"properties": "nope"},
		{"type": "object"},
		{},
	}
	for _, doc := range plain {
		if IsSchemaDocument(doc) {
			t.Errorf("%#v read as a JSON Schema", doc)
		}
	}
}
