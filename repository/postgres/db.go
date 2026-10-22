// Package postgres is the postgres-backed implementation of repository.Store.
//
// It is the optional backend for deployments that have outgrown the sqlite
// default: several API replicas against one database, an existing managed
// postgres, backup/replication that the ops team already runs. Nothing in the
// application changes — it registers itself as the "postgres" driver and
// satisfies the same repository interfaces — so selecting it is a matter of
// config (DB_DRIVER/DB_SOURCE, or the database panel's switch).
//
// Vector memory stays on sqlite-vec even here. The store rows and the document
// tables live in postgres, but embeddings are indexed in a sqlite file
// (the "vector sidecar", see VectorSource), which keeps pgvector out of the
// requirements and leaves existing vector stores exactly where they already
// were. Queries are hand-written rather than sqlc-generated: a second sqlc
// engine would mean a second generated package to keep in step for no gain,
// since these statements are the sqlite ones with postgres placeholders.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/seed"
	"github.com/FloMorphic/morph-api/repository/sqlite"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaSQL string

const driverName = "postgres"

func init() {
	repository.Register(driverName, Open)
	repository.RegisterProber(driverName, Probe)
}

// vectorSource overrides where the vector sidecar lives. Empty means "ask the
// environment" (DB_VECTOR_SOURCE, else the deployment's sqlite file); the
// sentinel VectorDisabled turns vector memory off for this backend.
var vectorSource atomic.Value // string

// VectorDisabled is the vectorSource value that declines to open any sidecar.
// Vector memory stores then report a clear error instead of half-working.
const VectorDisabled = "off"

// SetVectorSource points the vector sidecar at a sqlite file (or VectorDisabled)
// before any store is opened. It exists because repository.Opener takes a single
// data source — the postgres URI — and the sidecar is a second one.
func SetVectorSource(path string) { vectorSource.Store(strings.TrimSpace(path)) }

// resolveVectorSource is the sidecar path a new store should use.
func resolveVectorSource() string {
	if v, ok := vectorSource.Load().(string); ok && v != "" {
		return v
	}
	return env.GetVectorDBSource()
}

// store implements repository.Store over a single *sql.DB.
type store struct {
	db  *sql.DB
	vec *sqlite.VectorIndex
	// local serves the entities DB_LOCAL_TABLES pinned to sqlite (nil when
	// postgres carries everything) — see residency.go.
	local        *localResidency
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

// Open connects to the postgres database at source (a postgres:// URI or a
// libpq key/value DSN), applies the schema and returns the Store. It also opens
// the vector sidecar, unless vector memory is switched off — a sidecar that
// cannot be opened is a warning, not a startup failure, so a deployment with no
// writable sqlite path still serves everything except vector memory.
func Open(source string) (repository.Store, error) {
	dsn := strings.TrimSpace(source)
	if dsn == "" {
		return nil, fmt.Errorf("postgres: a connection URI is required (postgres://user:pass@host:5432/db)")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	// A single API process is not a connection-hungry workload, and a bounded
	// pool is the polite thing to do against a shared/managed server.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: apply schema: %w", err)
	}
	if err := applyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: apply migrations: %w", err)
	}

	// Vector sidecar: embeddings stay in sqlite-vec (see the package comment).
	var vec *sqlite.VectorIndex
	if src := resolveVectorSource(); src != "" && !strings.EqualFold(src, VectorDisabled) {
		if idx, err := sqlite.OpenVectorIndex(src); err != nil {
			fmt.Printf("warning: vector memory unavailable: %v\n", err)
		} else {
			vec = idx
			version, _ := idx.Version(ctx)
			fmt.Printf("postgres ready (%s), vector index on sqlite %s (sqlite-vec %s)\n",
				repository.RedactSource(dsn), src, version)
		}
	} else {
		fmt.Printf("postgres ready (%s), vector memory disabled\n", repository.RedactSource(dsn))
	}

	local, err := openLocalResidency(resolveVectorSource())
	if err != nil {
		if vec != nil {
			_ = vec.Close()
		}
		_ = db.Close()
		return nil, err
	}

	s := &store{
		db:           db,
		vec:          vec,
		local:        local,
		workflows:    &workflowRepo{db: db},
		contexts:     &contextRepo{db: db},
		memory:       &memoryRepo{db: db, vec: vec},
		prompts:      &promptRepo{db: db},
		humanTasks:   &humanTaskRepo{db: db},
		nodeSettings: &nodeSettingRepo{db: db},
		processes:    &processRepo{db: db},
		extensions:   &extensionRepo{db: db},
		triggers:     &triggerRepo{db: db},
		connect:      &connectRepo{db: db},
		tgRecipients: &telegramRecipientRepo{db: db},
	}

	// Same first-run seeding as every other backend: the builtin palette and the
	// starter prompts, idempotent and never fatal. The accessors are used rather
	// than the postgres repositories directly, so an entity pinned to sqlite is
	// seeded there (its own Open already did, harmlessly twice) instead of being
	// seeded into a postgres table nothing reads.
	seed.Apply(context.Background(), s.Extensions(), s.Prompts())

	return s, nil
}

// Each accessor hands back the postgres repository unless the entity was pinned
// to sqlite, in which case the local store answers for it.

func (s *store) Workflows() repository.WorkflowRepository {
	if s.local.holds(EntityWorkflows) {
		return s.local.store.Workflows()
	}
	return s.workflows
}

func (s *store) Contexts() repository.ContextRepository {
	if s.local.holds(EntityContexts) {
		return s.local.store.Contexts()
	}
	return s.contexts
}

func (s *store) Memory() repository.MemoryRepository {
	if s.local.holds(EntityMemory) {
		return s.local.store.Memory()
	}
	return s.memory
}

func (s *store) Prompts() repository.PromptRepository {
	if s.local.holds(EntityPrompts) {
		return s.local.store.Prompts()
	}
	return s.prompts
}

func (s *store) HumanTasks() repository.HumanTaskRepository {
	if s.local.holds(EntityHumanTasks) {
		return s.local.store.HumanTasks()
	}
	return s.humanTasks
}

func (s *store) NodeSettings() repository.NodeSettingRepository {
	if s.local.holds(EntityNodeSettings) {
		return s.local.store.NodeSettings()
	}
	return s.nodeSettings
}

func (s *store) Connect() repository.ConnectRepository {
	if s.local.holds(EntityConnect) {
		return s.local.store.Connect()
	}
	return s.connect
}

func (s *store) Processes() repository.ProcessRepository {
	if s.local.holds(EntityProcesses) {
		return s.local.store.Processes()
	}
	return s.processes
}

func (s *store) Extensions() repository.ExtensionRepository {
	if s.local.holds(EntityExtensions) {
		return s.local.store.Extensions()
	}
	return s.extensions
}

func (s *store) Triggers() repository.TriggerRepository {
	if s.local.holds(EntityTriggers) {
		return s.local.store.Triggers()
	}
	return s.triggers
}

func (s *store) TelegramRecipients() repository.TelegramRecipientRepository {
	if s.local.holds(EntityTelegram) {
		return s.local.store.TelegramRecipients()
	}
	return s.tgRecipients
}

// LocalEntities reports which entities this store serves from sqlite, for the
// database panel to show. Empty means postgres carries everything.
func (s *store) LocalEntities() []string { return s.local.names() }

// Ping reports whether the database still answers — the database panel's health
// check, through repository.Pinger.
func (s *store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close releases the connection pool, the vector sidecar and the local entity
// store. A failure to close either sqlite side is reported but never hides the
// postgres close, which is the one the caller is waiting on.
func (s *store) Close() error {
	if s.vec != nil {
		if err := s.vec.Close(); err != nil {
			fmt.Printf("warning: closing vector index: %v\n", err)
		}
	}
	if err := s.local.close(); err != nil {
		fmt.Printf("warning: closing local entity store: %v\n", err)
	}
	return s.db.Close()
}

// applyMigrations runs additive schema changes on a database created by an
// earlier version. postgres takes `ADD COLUMN IF NOT EXISTS`, so unlike the
// sqlite backend each statement is idempotent on its own (ADD COLUMN IF NOT
// EXISTS) and no error has to be swallowed. New columns go here as well as into
// schema.sql, which never touches an existing table.
func applyMigrations(ctx context.Context, db *sql.DB) error {
	migrations := []string{
		`ALTER TABLE workflows ADD COLUMN IF NOT EXISTS tags TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE workflows ADD COLUMN IF NOT EXISTS preferences TEXT NOT NULL DEFAULT '{}'`,
	}
	for _, stmt := range migrations {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%q: %w", stmt, err)
		}
	}
	return nil
}

// Probe inspects a candidate postgres without taking the application onto it:
// it connects, reads the server version, checks this role may actually create
// objects in the target schema, and reports whether the vector sidecar is
// available. It creates no application table — an operator who mistypes a URI
// leaves no trace in whatever database they hit.
func Probe(ctx context.Context, source string) (repository.ProbeResult, error) {
	out := repository.ProbeResult{Driver: driverName}
	dsn := strings.TrimSpace(source)
	if dsn == "" {
		return out, fmt.Errorf("postgres: a connection URI is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return out, fmt.Errorf("postgres: open: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return out, fmt.Errorf("postgres: cannot reach the server: %w", err)
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err == nil {
		// "PostgreSQL 16.2 (Debian …) on x86_64…" — the first two words are the
		// useful part for a panel line.
		if fields := strings.Fields(version); len(fields) >= 2 {
			out.Server = fields[0] + " " + fields[1]
		} else {
			out.Server = version
		}
	}
	// CREATE privilege on the schema the search_path lands on: an operator who
	// pointed us at a read-only replica or a locked-down schema should learn now,
	// not on the first save.
	var writable bool
	if err := db.QueryRowContext(ctx,
		"SELECT has_schema_privilege(current_schema(), 'CREATE')").Scan(&writable); err != nil {
		out.Notes = append(out.Notes, "could not check write privileges: "+err.Error())
	}
	out.Writable = writable
	if !writable {
		out.Notes = append(out.Notes, "this role cannot create tables in the target schema")
	}
	// Report whether the application's tables are already there, so the panel can
	// say "existing FloMorphic database" rather than implying a fresh one.
	var hasSchema bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'workflows')").
		Scan(&hasSchema); err == nil && hasSchema {
		out.Notes = append(out.Notes, "the FloMorphic schema is already present in this database")
	}

	// The vector sidecar is a property of this deployment, not of the server, but
	// it decides whether vector memory will work after the switch — so it belongs
	// in the same answer.
	src := resolveVectorSource()
	switch {
	case src == "" || strings.EqualFold(src, VectorDisabled):
		out.Notes = append(out.Notes, "vector memory is disabled (no vector sidecar configured)")
	default:
		idx, err := sqlite.OpenVectorIndex(src)
		if err != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("vector memory unavailable: %v", err))
			break
		}
		defer idx.Close()
		out.Vector = true
		out.Notes = append(out.Notes, fmt.Sprintf("vector indexes stay in sqlite-vec at %s", src))
	}
	return out, nil
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// clampLimit applies a sane default/upper bound to a page size coming from the
// repository layer. Identical to the sqlite backend's, so a page is the same
// size whichever database answers it.
func clampLimit(limit int) int {
	if limit <= 0 {
		return 12
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// notFound maps a no-rows error to repository.ErrNotFound and passes anything
// else through, so controllers get their 404 regardless of backend.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ErrNotFound
	}
	return err
}
