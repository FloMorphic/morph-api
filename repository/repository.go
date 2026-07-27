// Package repository defines the persistence contract for FloMorphic's entities
// and a small driver registry. Concrete backends (see repository/sqlite) satisfy
// these interfaces and register themselves, so the API and controllers depend
// only on the abstraction — adding e.g. a postgres backend needs no changes
// outside its own package.
package repository

import (
	"context"
	"fmt"
	"sort"

	"github.com/FloMorphic/morph-api/models"
)

// ListParams carries page-based pagination (offset/limit) and an optional
// search term to the list methods. Status is an optional, entity-specific filter
// (currently used by HumanTasks); empty means "any".
type ListParams struct {
	Offset int
	Limit  int
	Search string
	Status string
	// NodeUniqID is an optional filter used by NodeSettings to scope a list to
	// the profiles of one node (its kind / plugin identity); empty means "any".
	NodeUniqID string
	// PID is an optional filter used by Processes to scope a list to the rows of
	// one engine process uuid; empty means "any".
	PID string
	// FlowID is an optional filter used by Processes to scope a list to the runs
	// of one workflow (e.g. the running processes shown while editing it); empty
	// means "any".
	FlowID string
	// Kind is an optional filter used by Extensions to scope a list to one
	// origin ("builtin" or "extension"); empty means "any". The admin settings
	// panel lists builtins; the extension portal lists extensions.
	Kind string
}

// CRUD is the shared persistence contract for entities keyed by a string id
// with upsert semantics and paginated listing. Entity repositories that are
// plain CRUD alias an instantiation of it; those with extra methods embed it.
// Memory and Process stay outside it on purpose: Memory has no pagination and
// splits Create from its document/vector ops, and Process is keyed by an
// auto-increment int64 with a Create/Update split.
type CRUD[T any] interface {
	// Upsert inserts or updates the entity. On insert (empty ID) it assigns an
	// ID and CreatedAt; on update it preserves the original CreatedAt.
	Upsert(ctx context.Context, v *T) error
	GetByID(ctx context.Context, id string) (*T, error)
	// List returns a page ordered newest-first plus the total number of rows
	// matching the filters (across all pages).
	List(ctx context.Context, p ListParams) (items []T, total int64, err error)
	Delete(ctx context.Context, id string) error
}

// WorkflowRepository is CRUD for saved workflows (FlowRecord).
type WorkflowRepository = CRUD[models.FlowRecord]

// ContextRepository is CRUD for context documents (ContextRecord).
type ContextRepository = CRUD[models.ContextRecord]

// MemoryRepository is CRUD for memory stores (MemoryStore). Vector stores also
// carry a backing similarity index the implementation provisions/drops here.
type MemoryRepository interface {
	Create(ctx context.Context, m *models.MemoryStore) error
	GetByID(ctx context.Context, id string) (*models.MemoryStore, error)
	// List returns every store, newest-first. Memory is a small, un-paginated
	// collection in the web app, so there is no cursor.
	List(ctx context.Context) ([]models.MemoryStore, error)
	Delete(ctx context.Context, id string) error
	// RunReadQuery executes a document-store read against the given store and
	// returns the matched rows. The query MUST already have passed
	// models.ValidateReadSQL (single read-only statement) — the implementation
	// trusts that gate and only adds a read-only transaction as defense in
	// depth. The store is passed, not just the SQL, so the database a store is
	// bound to can be resolved per-store (today: the single app DB).
	RunReadQuery(ctx context.Context, store *models.MemoryStore, query string) ([]map[string]any, error)
	// WriteDocument stores one JSON document in the store's backing table and
	// returns its generated id. There is no SQL injection surface: the table
	// name comes from the (validated) store config and the document is bound as
	// a single JSON parameter, so the payload's shape is never interpreted.
	WriteDocument(ctx context.Context, store *models.MemoryStore, doc map[string]any) (string, error)
	// UpdateDocument replaces the JSON document stored under id in the store's
	// backing table, bumping updated_at. It returns ErrNotFound when no row has
	// that id. Injection-safety matches WriteDocument: the table name is
	// validated and the document is bound as a single JSON parameter.
	UpdateDocument(ctx context.Context, store *models.MemoryStore, id string, doc map[string]any) error
	// DeleteDocument removes the document with the given id from the store's
	// backing table, returning ErrNotFound when no row matched.
	DeleteDocument(ctx context.Context, store *models.MemoryStore, id string) error
	// IndexVector stores one already-embedded vector (with its source text and
	// optional metadata) in the store's sqlite-vec index and returns the id it
	// was stored under. The caller produces the vector from the store's captured
	// embedding config; the implementation only checks its width matches the
	// index and binds every value as a parameter. partition is an optional
	// per-record key (a namespace/tag) SearchVectors can later filter on; "" stores
	// the record un-partitioned.
	IndexVector(ctx context.Context, store *models.MemoryStore, content string, vector []float32, metadata map[string]any, partition string) (string, error)
	// SearchVectors runs a k-nearest-neighbour search over the store's index for
	// the given query vector and returns the closest matches (by the store's
	// configured metric), nearest first. k is clamped to a sane bound by the
	// implementation. When partition is non-empty the search is restricted to
	// records stored under that partition key, so the top-k is computed within the
	// partition rather than across the whole index.
	SearchVectors(ctx context.Context, store *models.MemoryStore, vector []float32, k int, partition string) ([]models.VectorMatch, error)
}

// PromptRepository is CRUD for prompt templates (PromptRecord).
//
// A starter set is additionally seeded on first run; SeedPrompts inserts those
// definitions idempotently (keyed by id), leaving existing rows untouched.
type PromptRepository interface {
	CRUD[models.PromptRecord]
	// SeedPrompts inserts any prompt in defs whose id is not already present.
	// Seeded prompts carry a fixed id precisely so a restart recognises them and
	// never inserts a second copy — and so edits to one survive. A prompt the
	// user deletes does come back on the next start: that is the trade for
	// keeping the set fixed and the check id-based. Returns the rows inserted.
	SeedPrompts(ctx context.Context, defs []models.PromptRecord) (int, error)
}

// HumanTaskRepository is CRUD for Human-in-the-Loop tasks (HumanTask).
//
// Upsert is intentionally part of the repository but NOT the REST API: HITL
// tasks are only ever created/updated by the inflow svc handler when a running
// workflow reaches a `humanInLoop` node. The API exposes reads, the answer /
// message / close actions, and delete.
type HumanTaskRepository interface {
	CRUD[models.HumanTask]
	// Answer records an answer to a single question, flipping the task to
	// `answered` once every question has an answer. Returns the updated task.
	Answer(ctx context.Context, id, questionID, answer string) (*models.HumanTask, error)
	// AppendMessage adds one turn to the task's chat thread and returns the
	// updated task.
	AppendMessage(ctx context.Context, id string, msg models.HumanTaskMessage) (*models.HumanTask, error)
	// Close force-finishes the task (the workflow terminates at this step).
	Close(ctx context.Context, id string) (*models.HumanTask, error)
}

// NodeSettingRepository is CRUD for node settings profiles (NodeSetting). The
// list accepts an optional NodeUniqID filter (via ListParams) so the node drawer
// can fetch just that node's profiles.
type NodeSettingRepository = CRUD[models.NodeSetting]

// ProcessRepository is CRUD for process runs (Process). Rows are written by the
// inflow layer when a process request is sent and closed out from the engine's
// proc.finish event, so the API exposes a launch action, reads and delete rather
// than a plain create.
//
// Create and Update are split (rather than a single Upsert) because identity is
// an auto-increment integer: a launch must Create the row to learn its indexId,
// echo that index into the ProcessRequest meta, then Update the row with the
// request before dispatching.
type ProcessRepository interface {
	// Create inserts a new run and populates p.IndexID with the assigned index.
	Create(ctx context.Context, p *models.Process) error
	// Update writes an existing run, keyed by p.IndexID.
	Update(ctx context.Context, p *models.Process) error
	GetByIndex(ctx context.Context, indexID int64) (*models.Process, error)
	List(ctx context.Context, params ListParams) (items []models.Process, total int64, err error)
	Delete(ctx context.Context, indexID int64) error
	// GetRunningByPID returns the single `running` row for an engine process
	// uuid, newest-first, or ErrNotFound when none is running. It is how a
	// proc.finish event (which carries only the pid) resolves the exact row to
	// close, since only one row per pid is ever `running` at a time.
	GetRunningByPID(ctx context.Context, pid string) (*models.Process, error)
	// NextScheduled returns the `scheduled` row with the smallest ScheduledAt,
	// or ErrNotFound when nothing is waiting. The scheduler arms its timer from
	// it — the table (ordered by the scheduled index) is the priority queue.
	NextScheduled(ctx context.Context) (*models.Process, error)
	// ListDueScheduled returns every `scheduled` row whose ScheduledAt has been
	// reached (<= now, epoch millis), soonest first — the batch the scheduler
	// dispatches when its timer fires.
	ListDueScheduled(ctx context.Context, now int64) ([]models.Process, error)
}

// ExtensionRepository is CRUD for palette extensions (ExtensionRecord) — the
// node palette. The list accepts an optional Kind filter (via ListParams) so the
// admin settings panel and the extension portal can each scope to their origin.
//
// Builtins are additionally seeded on first run; SeedBuiltins upserts a set of
// builtin definitions idempotently (keyed by name), leaving existing rows and
// admin edits untouched.
type ExtensionRepository interface {
	CRUD[models.ExtensionRecord]
	// SeedBuiltins inserts any builtin in defs that is not already present
	// (matched by name). It never overwrites an existing builtin, so admin edits
	// survive restarts. Returns the number of rows inserted.
	SeedBuiltins(ctx context.Context, defs []models.ExtensionRecord) (int, error)
}

// Store aggregates the per-entity repositories behind one handle and owns the
// underlying connection lifecycle.
type Store interface {
	Workflows() WorkflowRepository
	Contexts() ContextRepository
	Memory() MemoryRepository
	Prompts() PromptRepository
	HumanTasks() HumanTaskRepository
	NodeSettings() NodeSettingRepository
	Processes() ProcessRepository
	Extensions() ExtensionRepository
	Close() error
}

// Opener constructs a Store from a driver-specific data source.
type Opener func(source string) (Store, error)

var openers = map[string]Opener{}

// Register makes a driver available to Open. Backends call this from init().
func Register(driver string, opener Opener) {
	if opener == nil {
		panic("repository: Register opener is nil")
	}
	if _, dup := openers[driver]; dup {
		panic("repository: Register called twice for driver " + driver)
	}
	openers[driver] = opener
}

// Open opens the Store for the named driver. Import the driver package for its
// side-effecting init() before calling, e.g.:
//
//	import _ "github.com/FloMorphic/morph-api/repository/sqlite"
func Open(driver, source string) (Store, error) {
	opener, ok := openers[driver]
	if !ok {
		return nil, fmt.Errorf("repository: unknown driver %q (imported?)", driver)
	}
	return opener(source)
}

// Drivers lists the registered driver names, sorted.
func Drivers() []string {
	names := make([]string, 0, len(openers))
	for name := range openers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
