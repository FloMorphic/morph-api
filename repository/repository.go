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
// search term to the list methods.
type ListParams struct {
	Offset int
	Limit  int
	Search string
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

// Store aggregates the per-entity repositories behind one handle and owns the
// underlying connection lifecycle.
type Store interface {
	Workflows() WorkflowRepository
	Contexts() ContextRepository
	Memory() MemoryRepository
	Prompts() PromptRepository
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
