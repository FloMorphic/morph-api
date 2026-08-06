package sqlite

import (
	"context"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// A synced action row's declared outbound ports must survive the JSON column
// round-trip: the canvas reads them back to render one output port per entry.
func TestExtensionOutboundRoundTrip(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	exts := st.Extensions()

	rec := &models.ExtensionRecord{
		Kind:     models.KindExtension,
		Type:     models.ExtPluginBaseType,
		Name:     "Review",
		PluginID: "reviewer-1",
		Action:   "review",
		Outbound: []models.OutboundPort{
			{Title: "Approved", Tags: []string{"approved"}, Description: "passed review"},
			{Title: "Rejected", Tags: []string{"rejected", "notify"}},
		},
	}
	if err := exts.Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := exts.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Outbound) != 2 {
		t.Fatalf("outbound = %+v, want 2 ports", got.Outbound)
	}
	if got.Outbound[0].Title != "Approved" || got.Outbound[0].Description != "passed review" {
		t.Errorf("outbound[0] = %+v, want the Approved port", got.Outbound[0])
	}
	if len(got.Outbound[1].Tags) != 2 || got.Outbound[1].Tags[1] != "notify" {
		t.Errorf("outbound[1] tags = %v, want [rejected notify]", got.Outbound[1].Tags)
	}
}

// A plugin action with no declared ports stores and reloads as an empty list, so
// the node keeps its single default source handle.
func TestExtensionOutboundEmpty(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	exts := st.Extensions()

	rec := &models.ExtensionRecord{Kind: models.KindExtension, Name: "Ping", PluginID: "p-1", Action: "ping"}
	if err := exts.Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := exts.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Outbound) != 0 {
		t.Errorf("outbound = %+v, want none", got.Outbound)
	}
}
