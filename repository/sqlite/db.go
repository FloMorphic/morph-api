// Package sqlite is the sqlite-backed implementation of repository.Store. It
// uses sqlc-generated queries (see ./sqlcgen) over the mattn/go-sqlite3 driver
// and loads the sqlite-vec extension so vector memory stores get a real
// similarity index. It registers itself as the "sqlite" driver on import.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/seed"
	"github.com/FloMorphic/morph-api/repository/sqlite/sqlcgen"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

const driverName = "sqlite"

func init() {
	// Make every new sqlite3 connection auto-load sqlite-vec (vec0 tables).
	sqlite_vec.Auto()
	repository.Register(driverName, Open)
	repository.RegisterProber(driverName, Probe)
}

// Probe inspects a candidate sqlite file without taking the application onto it:
// it creates the parent directory, opens the file, checks sqlite-vec loaded and
// that the connection can actually write. It applies no application schema and
// seeds nothing — an operator testing a path gets an empty file at worst, not a
// half-provisioned database.
func Probe(ctx context.Context, source string) (repository.ProbeResult, error) {
	out := repository.ProbeResult{Driver: driverName}
	if strings.TrimSpace(source) == "" {
		source = "flomorphic.db"
	}
	idx, err := OpenVectorIndex(source)
	if err != nil {
		return out, err
	}
	defer idx.Close()

	version, err := idx.Version(ctx)
	if err != nil {
		return out, err
	}
	out.Vector = true
	out.Server = fmt.Sprintf("SQLite (sqlite-vec %s) at %s", version, source)

	// A file in a read-only directory, or one owned by another user, opens fine
	// and then fails on the first save — so write something and roll it back.
	if _, err := idx.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _flomorphic_probe (x INTEGER)`); err != nil {
		out.Notes = append(out.Notes, "this database is not writable: "+err.Error())
		return out, nil
	}
	_, _ = idx.db.ExecContext(ctx, `DROP TABLE IF EXISTS _flomorphic_probe`)
	out.Writable = true

	// Report an existing FloMorphic database, so the panel can say "existing"
	// rather than implying a fresh one.
	var found string
	if err := idx.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'workflows'`).Scan(&found); err == nil && found != "" {
		out.Notes = append(out.Notes, "the FloMorphic schema is already present in this file")
	}
	return out, nil
}

// store implements repository.Store over a single *sql.DB.
type store struct {
	db           *sql.DB
	workflows    *workflowRepo
	contexts     *contextRepo
	memory       *memoryRepo
	prompts      *promptRepo
	humanTasks   *humanTaskRepo
	nodeSettings *nodeSettingRepo
	processes    *processRepo
	extensions   *extensionRepo
	triggers     *triggerRepo
	connect      *connectRepo
	tgRecipients *telegramRecipientRepo
}

// Open connects to the sqlite database at source (a file path for sqlite),
// applies the schema, verifies sqlite-vec is available, and returns the Store.
func Open(source string) (repository.Store, error) {
	if err := ensureDBDir(source); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", buildDSN(source))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	// SQLite serializes writers; one connection sidesteps "database is locked"
	// and keeps :memory: databases coherent. Fine for this service's scale.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: apply schema: %w", err)
	}
	if err := applyMigrations(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: apply migrations: %w", err)
	}
	vecVersion, err := newVectorIndex(db).Version(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	fmt.Printf("sqlite ready (%s), sqlite-vec %s\n", source, vecVersion)

	q := sqlcgen.New(db)
	s := &store{
		db:           db,
		workflows:    &workflowRepo{q: q},
		contexts:     &contextRepo{q: q},
		memory:       &memoryRepo{db: db, q: q, vec: newVectorIndex(db)},
		prompts:      &promptRepo{q: q},
		humanTasks:   &humanTaskRepo{q: q},
		nodeSettings: &nodeSettingRepo{q: q},
		processes:    &processRepo{q: q},
		extensions:   &extensionRepo{q: q},
		triggers:     &triggerRepo{q: q},
		connect:      &connectRepo{q: q},
		tgRecipients: &telegramRecipientRepo{q: q},
	}

	// Seed the builtin palette nodes and the starter prompt templates on first
	// run, from the shared seed package so every backend comes up with the same
	// set. Both passes are idempotent and never fatal.
	seed.Apply(context.Background(), s.extensions, s.prompts)

	return s, nil
}

// Ping reports whether the underlying database still answers — what the
// database panel's health check calls through repository.Pinger.
func (s *store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *store) Workflows() repository.WorkflowRepository       { return s.workflows }
func (s *store) Contexts() repository.ContextRepository         { return s.contexts }
func (s *store) Memory() repository.MemoryRepository            { return s.memory }
func (s *store) Prompts() repository.PromptRepository           { return s.prompts }
func (s *store) HumanTasks() repository.HumanTaskRepository     { return s.humanTasks }
func (s *store) NodeSettings() repository.NodeSettingRepository { return s.nodeSettings }
func (s *store) Connect() repository.ConnectRepository          { return s.connect }
func (s *store) Processes() repository.ProcessRepository        { return s.processes }
func (s *store) Extensions() repository.ExtensionRepository     { return s.extensions }
func (s *store) Triggers() repository.TriggerRepository         { return s.triggers }
func (s *store) TelegramRecipients() repository.TelegramRecipientRepository {
	return s.tgRecipients
}
func (s *store) Close() error { return s.db.Close() }

// applyMigrations runs additive, idempotent schema changes that cannot live in
// schema.sql. `CREATE TABLE IF NOT EXISTS` never alters a table that already
// exists, so columns added after a database was first created must be patched
// in here with `ALTER TABLE ... ADD COLUMN`. Each statement is expected to fail
// with "duplicate column name" on databases that already have the column (fresh
// databases, or ones migrated on a previous boot); that error is swallowed so
// the migration stays idempotent.
func applyMigrations(ctx context.Context, db *sql.DB) error {
	migrations := []string{
		`ALTER TABLE node_settings ADD COLUMN node_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE extensions ADD COLUMN install TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE extensions ADD COLUMN action TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE extensions ADD COLUMN parent_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE extensions ADD COLUMN outbound TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE human_tasks ADD COLUMN mode TEXT NOT NULL DEFAULT 'park'`,
		`ALTER TABLE human_tasks ADD COLUMN channel TEXT NOT NULL DEFAULT 'direct'`,
		`ALTER TABLE human_tasks ADD COLUMN prompt TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE human_tasks ADD COLUMN nexts TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE human_tasks ADD COLUMN settings_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE human_tasks ADD COLUMN node_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE human_tasks ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE human_tasks ADD COLUMN telegram TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE processes ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE processes ADD COLUMN snapshot TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE processes ADD COLUMN errors TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE connect_connections ADD COLUMN admin_token TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE workflows ADD COLUMN tags TEXT NOT NULL DEFAULT '[]'`,
	}
	for _, stmt := range migrations {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("%q: %w", stmt, err)
		}
	}
	return nil
}

// ensureDBDir creates the parent directory of a file-backed sqlite database if
// it is missing (sqlite errors rather than creating it). No-op for in-memory
// databases and for paths that live in the working directory.
func ensureDBDir(source string) error {
	if source == "" || strings.HasPrefix(source, ":memory:") || strings.Contains(source, "mode=memory") {
		return nil
	}
	// Strip any DSN query string and the optional file: scheme.
	path := source
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimPrefix(path, "file:")
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sqlite: create db dir %q: %w", dir, err)
	}
	return nil
}

// buildDSN appends pragmas for durability/concurrency to the raw source.
func buildDSN(source string) string {
	if strings.TrimSpace(source) == "" {
		source = "flomorphic.db"
	}
	sep := "?"
	if strings.Contains(source, "?") {
		sep = "&"
	}
	return source + sep + "_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on"
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// clampLimit applies a sane default/upper bound to a page size coming from the
// repository layer.
func clampLimit(limit int) int {
	if limit <= 0 {
		return 12
	}
	if limit > 100 {
		return 100
	}
	return limit
}
