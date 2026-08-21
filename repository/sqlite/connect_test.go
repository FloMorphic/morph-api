package sqlite

import (
	"context"
	"testing"

	"github.com/FloMorphic/morph-api/models"
)

// The default flag must stay single-valued as connections come and go: the first
// is default automatically, later ones don't steal it, SetDefault switches it,
// saving-as-default demotes the rest, and deleting the default promotes another.
func TestConnectDefaultLifecycle(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	repo := st.Connect()

	// First connection becomes default on its own.
	a := &models.ConnectConnection{Label: "A", BaseURL: "https://a", Token: "ta"}
	if err := repo.Upsert(ctx, a); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if !a.IsDefault {
		t.Fatalf("first connection should be default")
	}

	// Second one does not steal the default.
	b := &models.ConnectConnection{Label: "B", BaseURL: "https://b", Token: "tb"}
	if err := repo.Upsert(ctx, b); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	if b.IsDefault {
		t.Fatalf("second connection must not become default")
	}
	if def, _ := repo.Default(ctx); def == nil || def.ID != a.ID {
		t.Fatalf("default should still be A")
	}

	// SetDefault switches, single-valued.
	if err := repo.SetDefault(ctx, b.ID); err != nil {
		t.Fatalf("set default b: %v", err)
	}
	assertSingleDefault(t, repo, b.ID)

	// Saving A as default demotes B.
	a.IsDefault = true
	if err := repo.Upsert(ctx, a); err != nil {
		t.Fatalf("re-upsert a default: %v", err)
	}
	assertSingleDefault(t, repo, a.ID)

	// Editing A with an empty token must keep the stored token.
	a.Token = ""
	if err := repo.Upsert(ctx, a); err != nil {
		t.Fatalf("re-upsert a blank token: %v", err)
	}
	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	if got.Token != "ta" {
		t.Fatalf("stored token clobbered: got %q", got.Token)
	}

	// Deleting the default promotes the remaining connection.
	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatalf("delete a: %v", err)
	}
	assertSingleDefault(t, repo, b.ID)
}

func assertSingleDefault(t *testing.T, repo interface {
	List(context.Context) ([]models.ConnectConnection, error)
}, wantID string) {
	t.Helper()
	items, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defaults := 0
	for _, it := range items {
		if it.IsDefault {
			defaults++
			if it.ID != wantID {
				t.Fatalf("default is %s, want %s", it.ID, wantID)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default, got %d", defaults)
	}
}
