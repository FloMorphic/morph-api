package mcpserver

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// reqWith builds a CallToolRequest carrying the given arguments, mirroring how
// the transport delivers them (a map[string]any, numbers as float64).
func reqWith(args map[string]any) mcp.CallToolRequest {
	var r mcp.CallToolRequest
	r.Params.Arguments = args
	return r
}

func TestListParamsDefaults(t *testing.T) {
	p := listParams(reqWith(nil))
	if p.Offset != 0 {
		t.Errorf("default Offset = %d, want 0", p.Offset)
	}
	if p.Limit != 20 {
		t.Errorf("default Limit = %d, want 20", p.Limit)
	}
	if p.Search != "" {
		t.Errorf("default Search = %q, want empty", p.Search)
	}
}

func TestListParamsPaging(t *testing.T) {
	// page 3 at 25/page → offset 50, and the search term is trimmed.
	p := listParams(reqWith(map[string]any{
		"page":     float64(3),
		"per_page": float64(25),
		"search":   "  hello  ",
	}))
	if p.Offset != 50 {
		t.Errorf("Offset = %d, want 50", p.Offset)
	}
	if p.Limit != 25 {
		t.Errorf("Limit = %d, want 25", p.Limit)
	}
	if p.Search != "hello" {
		t.Errorf("Search = %q, want %q", p.Search, "hello")
	}
}

func TestListParamsBounds(t *testing.T) {
	// per_page above the cap is clamped to 100; a sub-1 page is floored to 1
	// (offset 0).
	p := listParams(reqWith(map[string]any{
		"page":     float64(0),
		"per_page": float64(9999),
	}))
	if p.Limit != 100 {
		t.Errorf("Limit = %d, want 100 (capped)", p.Limit)
	}
	if p.Offset != 0 {
		t.Errorf("Offset = %d, want 0", p.Offset)
	}
}

func TestArgObject(t *testing.T) {
	// Missing key → nil, no error.
	if m, err := argObject(reqWith(nil), "settings"); err != nil || m != nil {
		t.Errorf("missing key: got (%v, %v), want (nil, nil)", m, err)
	}

	// Present object → returned as-is.
	obj := map[string]any{"token": "abc"}
	if m, err := argObject(reqWith(map[string]any{"settings": obj}), "settings"); err != nil {
		t.Errorf("object: unexpected error %v", err)
	} else if m["token"] != "abc" {
		t.Errorf("object: got %v, want token=abc", m)
	}

	// Present non-object → error (a malformed payload is rejected, not dropped).
	if _, err := argObject(reqWith(map[string]any{"settings": "not-an-object"}), "settings"); err == nil {
		t.Error("non-object: expected an error, got nil")
	}
}

func TestValidReadSQLGate(t *testing.T) {
	// A bounded SELECT passes (and gains a LIMIT); a write is rejected. This only
	// asserts the gate is wired to models.ValidateReadSQL — its own rules are
	// covered by models' tests.
	if _, err := validReadSQL("SELECT id FROM docs"); err != nil {
		t.Errorf("SELECT rejected: %v", err)
	}
	if _, err := validReadSQL("DELETE FROM docs"); err == nil {
		t.Error("DELETE accepted, want rejection")
	}
}
