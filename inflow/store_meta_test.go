package inflow

import (
	"reflect"
	"testing"
)

// A vector node's metadata rows must be flattened onto the `op` payload as
// individual `meta.<key>` string entries so the engine resolves each value's
// {{$.path}} placeholder (only root-level op strings are resolved). Blank keys
// are dropped; values are carried verbatim.
func TestStoreMetaPayload(t *testing.T) {
	t.Run("row array from the drawer", func(t *testing.T) {
		raw := []any{
			map[string]any{"key": "env", "value": "{{$.env}}"},
			map[string]any{"key": "x", "value": "{{$.data.point.x}}"},
			map[string]any{"key": "", "value": "dropped"}, // blank key
			map[string]any{"value": "no key"},             // missing key
			"not a row",                                    // ignored
		}
		got := storeMetaPayload(raw)
		want := map[string]any{
			"meta.env": "{{$.env}}",
			"meta.x":   "{{$.data.point.x}}",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("storeMetaPayload = %v, want %v", got, want)
		}
	})

	t.Run("plain object shape", func(t *testing.T) {
		got := storeMetaPayload(map[string]any{"a": "1", "": "drop"})
		want := map[string]any{"meta.a": "1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("storeMetaPayload = %v, want %v", got, want)
		}
	})

	t.Run("nil or unknown is empty", func(t *testing.T) {
		if got := storeMetaPayload(nil); len(got) != 0 {
			t.Fatalf("storeMetaPayload(nil) = %v, want empty", got)
		}
	})
}
