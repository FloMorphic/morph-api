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

// WorkflowRepository is CRUD for saved workflows (FlowRecord).
type WorkflowRepository interface {
	// Upsert inserts or updates a workflow. On insert (empty ID) it assigns an
	// ID and CreatedAt; on update it preserves the original CreatedAt.
	Upsert(ctx context.Context, w *models.FlowRecord) error
	GetByID(ctx context.Context, id string) (*models.FlowRecord, error)
	// List returns a page ordered newest-first plus the total number of rows
	// matching the search (across all pages).
	List(ctx context.Context, p ListParams) (items []models.FlowRecord, total int64, err error)
	Delete(ctx context.Context, id string) error
}

// ContextRepository is CRUD for context documents (ContextRecord).
type ContextRepository interface {
	Upsert(ctx context.Context, c *models.ContextRecord) error
	GetByID(ctx context.Context, id string) (*models.ContextRecord, error)
	List(ctx context.Context, p ListParams) (items []models.ContextRecord, total int64, err error)
	Delete(ctx context.Context, id string) error
}

// MemoryRepository is CRUD for memory stores (MemoryStore). Vector stores also
// carry a backing similarity index the implementation provisions/drops here.
type MemoryRepository interface {
	Create(ctx context.Context, m *models.MemoryStore) error
	GetByID(ctx context.Context, id string) (*models.MemoryStore, error)
	// List returns every store, newest-first. Memory is a small, un-paginated
	// collection in the web app, so there is no cursor.
	List(ctx context.Context) ([]models.MemoryStore, error)
	Delete(ctx context.Context, id string) error
}

// PromptRepository is CRUD for prompt templates (PromptRecord).
type PromptRepository interface {
	Upsert(ctx context.Context, p *models.PromptRecord) error
	GetByID(ctx context.Context, id string) (*models.PromptRecord, error)
	List(ctx context.Context, params ListParams) (items []models.PromptRecord, total int64, err error)
	Delete(ctx context.Context, id string) error
}

// HumanTaskRepository is CRUD for Human-in-the-Loop tasks (HumanTask).
//
// Upsert is intentionally part of the repository but NOT the REST API: HITL
// tasks are only ever created/updated by the inflow svc handler when a running
// workflow reaches a `humanInLoop` node. The API exposes reads, the answer /
// message / close actions, and delete.
type HumanTaskRepository interface {
	Upsert(ctx context.Context, t *models.HumanTask) error
	GetByID(ctx context.Context, id string) (*models.HumanTask, error)
	List(ctx context.Context, p ListParams) (items []models.HumanTask, total int64, err error)
	Delete(ctx context.Context, id string) error
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
type NodeSettingRepository interface {
	Upsert(ctx context.Context, s *models.NodeSetting) error
	GetByID(ctx context.Context, id string) (*models.NodeSetting, error)
	List(ctx context.Context, p ListParams) (items []models.NodeSetting, total int64, err error)
	Delete(ctx context.Context, id string) error
}

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
}

// ExtensionRepository is CRUD for palette extensions (ExtensionRecord) — the
// node palette. The list accepts an optional Kind filter (via ListParams) so the
// admin settings panel and the extension portal can each scope to their origin.
//
// Builtins are additionally seeded on first run; SeedBuiltins upserts a set of
// builtin definitions idempotently (keyed by name), leaving existing rows and
// admin edits untouched.
type ExtensionRepository interface {
	Upsert(ctx context.Context, e *models.ExtensionRecord) error
	GetByID(ctx context.Context, id string) (*models.ExtensionRecord, error)
	List(ctx context.Context, p ListParams) (items []models.ExtensionRecord, total int64, err error)
	Delete(ctx context.Context, id string) error
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
