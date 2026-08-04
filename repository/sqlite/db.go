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

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/sqlite/sqlcgen"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/bytedance/sonic"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

//go:embed seed/builtins.json
var builtinsSeed []byte

//go:embed seed/prompts.json
var promptsSeed []byte

const driverName = "sqlite"

func init() {
	// Make every new sqlite3 connection auto-load sqlite-vec (vec0 tables).
	sqlite_vec.Auto()
	repository.Register(driverName, Open)
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
	var vecVersion string
	if err := db.QueryRow("SELECT vec_version()").Scan(&vecVersion); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: sqlite-vec not loaded: %w", err)
	}
	fmt.Printf("sqlite ready (%s), sqlite-vec %s\n", source, vecVersion)

	q := sqlcgen.New(db)
	s := &store{
		db:           db,
		workflows:    &workflowRepo{q: q},
		contexts:     &contextRepo{q: q},
		memory:       &memoryRepo{db: db, q: q},
		prompts:      &promptRepo{q: q},
		humanTasks:   &humanTaskRepo{q: q},
		nodeSettings: &nodeSettingRepo{q: q},
		processes:    &processRepo{q: q},
		extensions:   &extensionRepo{q: q},
	}

	// Seed builtin palette nodes on first run (idempotent, keyed by name). A
	// bad/empty seed file must not stop the server from serving CRUD.
	if defs, err := decodeBuiltinsSeed(); err != nil {
		fmt.Printf("warning: builtins seed not applied: %v\n", err)
	} else if n, err := s.extensions.SeedBuiltins(context.Background(), defs); err != nil {
		fmt.Printf("warning: builtins seed not applied: %v\n", err)
	} else if n > 0 {
		fmt.Printf("seeded %d builtin node(s)\n", n)
	}

	// Seed the starter prompt templates the same way (idempotent, keyed by their
	// fixed ids). Same rule: a bad seed file is a warning, never a startup error.
	if defs, err := decodePromptsSeed(); err != nil {
		fmt.Printf("warning: prompts seed not applied: %v\n", err)
	} else if n, err := s.prompts.SeedPrompts(context.Background(), defs); err != nil {
		fmt.Printf("warning: prompts seed not applied: %v\n", err)
	} else if n > 0 {
		fmt.Printf("seeded %d prompt(s)\n", n)
	}

	return s, nil
}

// decodeBuiltinsSeed parses the embedded builtins seed into records.
func decodeBuiltinsSeed() ([]models.ExtensionRecord, error) {
	var defs []models.ExtensionRecord
	if len(builtinsSeed) == 0 {
		return defs, nil
	}
	if err := sonic.Unmarshal(builtinsSeed, &defs); err != nil {
		return nil, fmt.Errorf("decode builtins seed: %w", err)
	}
	return defs, nil
}

// decodePromptsSeed parses the embedded prompt-template seed into records.
func decodePromptsSeed() ([]models.PromptRecord, error) {
	var defs []models.PromptRecord
	if len(promptsSeed) == 0 {
		return defs, nil
	}
	if err := sonic.Unmarshal(promptsSeed, &defs); err != nil {
		return nil, fmt.Errorf("decode prompts seed: %w", err)
	}
	return defs, nil
}

func (s *store) Workflows() repository.WorkflowRepository       { return s.workflows }
func (s *store) Contexts() repository.ContextRepository         { return s.contexts }
func (s *store) Memory() repository.MemoryRepository            { return s.memory }
func (s *store) Prompts() repository.PromptRepository           { return s.prompts }
func (s *store) HumanTasks() repository.HumanTaskRepository     { return s.humanTasks }
func (s *store) NodeSettings() repository.NodeSettingRepository { return s.nodeSettings }
func (s *store) Processes() repository.ProcessRepository        { return s.processes }
func (s *store) Extensions() repository.ExtensionRepository      { return s.extensions }
func (s *store) Close() error                                   { return s.db.Close() }

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
		`ALTER TABLE human_tasks ADD COLUMN mode TEXT NOT NULL DEFAULT 'park'`,
		`ALTER TABLE human_tasks ADD COLUMN channel TEXT NOT NULL DEFAULT 'direct'`,
		`ALTER TABLE human_tasks ADD COLUMN prompt TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE human_tasks ADD COLUMN nexts TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE human_tasks ADD COLUMN settings_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE human_tasks ADD COLUMN node_key TEXT NOT NULL DEFAULT ''`,
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
