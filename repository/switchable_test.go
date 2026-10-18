package repository_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	_ "github.com/FloMorphic/morph-api/repository/sqlite"
)

// A switch must be invisible to everything holding the store: the repositories
// a controller resolved before the switch have to land on the new database
// afterwards, which is the whole reason the facade exists.
func TestSwitchableRepointsResolvedRepositories(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.db")
	second := filepath.Join(dir, "second.db")

	cfg := repository.Config{Driver: "sqlite", Source: first}
	opened, err := repository.Open(cfg.Driver, cfg.Source)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	st := repository.NewSwitchable(cfg, opened)
	defer st.Close()

	ctx := context.Background()
	// Resolved once, up front — as a controller does at registration time.
	flows := st.Workflows()

	rec := &models.FlowRecord{Title: "on first"}
	if err := flows.Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert on first: %v", err)
	}

	switched := false
	st.OnSwitch(func() { switched = true })
	if err := st.Switch(repository.Config{Driver: "sqlite", Source: second}); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if !switched {
		t.Fatalf("OnSwitch hook did not run")
	}
	if got := st.Config(); got.Source != second {
		t.Fatalf("live config is %q, want %q", got.Source, second)
	}

	// The handle resolved before the switch must now read the new database,
	// where that workflow was never written.
	if _, err := flows.GetByID(ctx, rec.ID); !repository.IsNotFound(err) {
		t.Fatalf("stale handle still reads the old database: err=%v", err)
	}
	items, total, err := flows.List(ctx, repository.ListParams{Limit: 10})
	if err != nil {
		t.Fatalf("list on second: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("new database should be empty, got %d rows (total %d)", len(items), total)
	}

	// Writing through the same handle lands on the new database, and switching
	// back finds the original row intact.
	if err := flows.Upsert(ctx, &models.FlowRecord{Title: "on second"}); err != nil {
		t.Fatalf("upsert on second: %v", err)
	}
	if err := st.Switch(cfg); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	back, err := flows.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get after switching back: %v", err)
	}
	if back.Title != "on first" {
		t.Fatalf("title is %q, want %q", back.Title, "on first")
	}
}

// A switch that cannot be opened must leave the instance exactly where it was —
// a mistyped URI costs an error message, never the service.
func TestSwitchableKeepsCurrentStoreOnFailure(t *testing.T) {
	cfg := repository.Config{Driver: "sqlite", Source: ":memory:"}
	opened, err := repository.Open(cfg.Driver, cfg.Source)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st := repository.NewSwitchable(cfg, opened)
	defer st.Close()

	ctx := context.Background()
	rec := &models.FlowRecord{Title: "keep me"}
	if err := st.Workflows().Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := st.Switch(repository.Config{Driver: "nosuchdb", Source: "whatever"}); err == nil {
		t.Fatalf("switch to an unknown driver should fail")
	}
	if err := st.Switch(repository.Config{Driver: "postgres"}); err == nil {
		t.Fatalf("switch to postgres with no URI should fail")
	}
	if got := st.Config().Source; got != ":memory:" {
		t.Fatalf("live config changed to %q after a failed switch", got)
	}
	if _, err := st.Workflows().GetByID(ctx, rec.ID); err != nil {
		t.Fatalf("current database unusable after a failed switch: %v", err)
	}
}

// The source travels to a browser, so any password in it must be masked —
// whatever shape the source arrived in.
func TestRedactSource(t *testing.T) {
	cases := []struct {
		in       string
		contains string
		absent   string
	}{
		{"postgres://flo:s3cret@db:5432/flomorphic", "***", "s3cret"},
		{"postgres://flo@db:5432/flomorphic", "flo@db", "***"},
		{"host=db port=5432 user=flo password=s3cret dbname=flo", "***", "s3cret"},
		{"host=db password='s3 cret' dbname=flo", "***", "s3 cret"},
		{"db/flomorphic.db", "db/flomorphic.db", "***"},
	}
	for _, tc := range cases {
		got := repository.RedactSource(tc.in)
		if !strings.Contains(got, tc.contains) {
			t.Errorf("RedactSource(%q) = %q, want it to contain %q", tc.in, got, tc.contains)
		}
		if tc.absent != "" && strings.Contains(got, tc.absent) {
			t.Errorf("RedactSource(%q) = %q, must not contain %q", tc.in, got, tc.absent)
		}
	}
}

// A saved selection is what a restart reads, so it has to round-trip — and the
// spellings an operator might paste have to normalize onto a registered driver.
func TestConfigRoundTripAndNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "database.json")
	if saved, err := repository.LoadConfig(path); err != nil || saved != nil {
		t.Fatalf("missing config should be (nil, nil), got (%v, %v)", saved, err)
	}

	in := repository.Config{Driver: "PostgreSQL", Source: "postgres://flo@db/flo"}
	written, err := repository.SaveConfig(path, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if written.Driver != "postgres" {
		t.Fatalf("driver normalized to %q, want postgres", written.Driver)
	}
	if written.UpdatedAt == 0 {
		t.Fatalf("save should stamp UpdatedAt")
	}

	loaded, err := repository.LoadConfig(path)
	if err != nil || loaded == nil {
		t.Fatalf("load: (%v, %v)", loaded, err)
	}
	if loaded.Driver != "postgres" || loaded.Source != in.Source {
		t.Fatalf("loaded %+v, want %+v", *loaded, written)
	}

	if err := repository.RemoveConfig(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Removing twice is still a success — the end state is what was asked for.
	if err := repository.RemoveConfig(path); err != nil {
		t.Fatalf("remove again: %v", err)
	}
	if saved, err := repository.LoadConfig(path); err != nil || saved != nil {
		t.Fatalf("config should be gone, got (%v, %v)", saved, err)
	}
}

// A connection URI whose password was not percent-encoded is what operators
// actually paste (it is what their password manager shows them). It must be
// repaired rather than failing on a host nobody typed, and a URI that is already
// well-formed must come back byte for byte.
func TestConfigNormalizesPastedURIPassword(t *testing.T) {
	cases := []struct{ in, want string }{
		// The '@' in the password: everything between the first ':' and the last
		// '@' is the password, so it is escaped and the host is left alone.
		{
			"postgres://postgres:mate@123@localhost:5432/flomorphic?sslmode=disable",
			"postgres://postgres:mate%40123@localhost:5432/flomorphic?sslmode=disable",
		},
		// Already encoded: untouched (never double-encoded).
		{
			"postgres://postgres:mate%40123@localhost:5432/flomorphic?sslmode=disable",
			"postgres://postgres:mate%40123@localhost:5432/flomorphic?sslmode=disable",
		},
		// No password, no userinfo, a key/value DSN and a file path: all untouched.
		{"postgres://postgres@localhost:5432/flo", "postgres://postgres@localhost:5432/flo"},
		{"postgres://localhost:5432/flo", "postgres://localhost:5432/flo"},
		{"host=localhost user=flo password=mate@123", "host=localhost user=flo password=mate@123"},
		{"db/flomorphic.db", "db/flomorphic.db"},
	}
	for _, tc := range cases {
		got := repository.Config{Driver: "postgres", Source: tc.in}.Normalized().Source
		if got != tc.want {
			t.Errorf("Normalized(%q).Source =\n  %q\nwant\n  %q", tc.in, got, tc.want)
		}
	}

	// The repaired URI must also redact cleanly — no fragment of the password
	// may survive the mask.
	red := repository.RedactSource("postgres://postgres:mate@123@localhost:5432/flomorphic")
	if red != "postgres://postgres:***@localhost:5432/flomorphic" {
		t.Errorf("RedactSource of a raw-'@' URI = %q", red)
	}
}
