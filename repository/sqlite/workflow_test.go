package sqlite

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

// TestWorkflowTags covers the three things the tag filter rests on: tags are
// normalized on the way in, a save that says nothing about them does not strip
// them, and the list filter is a match-ANY membership test that pages
// correctly (the count must agree with the rows).
func TestWorkflowTags(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	flows := store.Workflows()

	mk := func(title string, tags []string) *models.FlowRecord {
		rec := &models.FlowRecord{Title: title, Tags: tags}
		if err := flows.Upsert(ctx, rec); err != nil {
			t.Fatalf("upsert %q: %v", title, err)
		}
		return rec
	}

	// Normalization: case, padding, an inner space, a comma (which would break
	// the delimited filter), and a duplicate.
	messy := mk("messy", []string{" GDPR ", "ISO 27001", "a,b", "gdpr", ""})
	if got, want := strings.Join(messy.Tags, "|"), "gdpr|iso-27001|ab"; got != want {
		t.Fatalf("normalized tags = %q, want %q", got, want)
	}

	fraud := mk("fraud sweep", []string{"fraud-surveillance"})
	credit := mk("credit watch", []string{"credit-risk", "fraud-surveillance"})
	mk("untagged helper", nil)

	// A re-read carries the tags back.
	again, err := flows.GetByID(ctx, credit.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Join(again.Tags, "|") != "credit-risk|fraud-surveillance" {
		t.Fatalf("stored tags = %v", again.Tags)
	}

	// A save with nil tags (an MCP patch, an older client) keeps them; an
	// explicit empty list clears them.
	again.Tags = nil
	again.Title = "credit watch v2"
	if err := flows.Upsert(ctx, again); err != nil {
		t.Fatalf("upsert without tags: %v", err)
	}
	if strings.Join(again.Tags, "|") != "credit-risk|fraud-surveillance" {
		t.Fatalf("tags lost on a tag-less save: %v", again.Tags)
	}

	// Rows created in the same millisecond tie on `updated_at` and fall back to
	// a random id, so compare sets of titles, not order.
	list := func(tags ...string) []string {
		items, total, err := flows.List(ctx, repository.ListParams{Limit: 50, Tags: tags})
		if err != nil {
			t.Fatalf("list %v: %v", tags, err)
		}
		if int(total) != len(items) {
			t.Fatalf("list %v: total %d but %d rows", tags, total, len(items))
		}
		titles := make([]string, 0, len(items))
		for _, it := range items {
			titles = append(titles, it.Title)
		}
		sort.Strings(titles)
		return titles
	}

	// No filter is the whole list — clearing the filter needs no second path.
	if got := list(); len(got) != 4 {
		t.Fatalf("unfiltered list = %v, want 4 rows", got)
	}
	if got := strings.Join(list("fraud-surveillance"), "|"); got != "credit watch v2|fraud sweep" {
		t.Fatalf("one-tag filter = %q", got)
	}
	// Match ANY, not all.
	if got := len(list("credit-risk", "fraud-surveillance")); got != 2 {
		t.Fatalf("two-tag filter returned %d rows, want 2", got)
	}
	// Unknown tags match nothing, and the filter is exact per element: a tag
	// that is a prefix of a stored one must not match it.
	if got := list("nope"); len(got) != 0 {
		t.Fatalf("unknown tag matched %v", got)
	}
	if got := list("fraud"); len(got) != 0 {
		t.Fatalf("prefix tag matched %v", got)
	}
	// A filter the caller typed loosely still matches.
	if got := len(list(" FRAUD-Surveillance ")); got != 2 {
		t.Fatalf("loose filter returned %d rows, want 2", got)
	}

	// Clearing: an explicit empty list drops them, and the row leaves the filter.
	fraud.Tags = []string{}
	if err := flows.Upsert(ctx, fraud); err != nil {
		t.Fatalf("clear tags: %v", err)
	}
	if got := strings.Join(list("fraud-surveillance"), "|"); got != "credit watch v2" {
		t.Fatalf("after clearing, filter = %q", got)
	}
}
