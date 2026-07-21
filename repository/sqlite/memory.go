package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/sqlite/sqlcgen"
)

type memoryRepo struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

func (r *memoryRepo) Create(ctx context.Context, m *models.MemoryStore) error {
	now := nowMillis()
	if m.ID == "" {
		m.ID = repository.NewID(repository.MemoryIDPrefix)
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	m.UpdatedAt = now

	var vecCfg, docCfg sql.NullString
	switch m.Type {
	case models.MemoryVector:
		if m.Vector != nil {
			b, err := json.Marshal(m.Vector)
			if err != nil {
				return fmt.Errorf("sqlite: marshal vector config: %w", err)
			}
			vecCfg = sql.NullString{String: string(b), Valid: true}
		}
	case models.MemoryDocument:
		if m.Document != nil {
			b, err := json.Marshal(m.Document)
			if err != nil {
				return fmt.Errorf("sqlite: marshal document config: %w", err)
			}
			docCfg = sql.NullString{String: string(b), Valid: true}
		}
	}

	if err := r.q.CreateMemoryStore(ctx, sqlcgen.CreateMemoryStoreParams{
		ID:             m.ID,
		Name:           m.Name,
		Type:           string(m.Type),
		Description:    m.Description,
		VectorConfig:   vecCfg,
		DocumentConfig: docCfg,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}); err != nil {
		return err
	}

	// Provision the sqlite-vec index backing a vector store. If it fails, roll
	// back the row so we never leave a vector store without its index.
	if m.Type == models.MemoryVector && m.Vector != nil && m.Vector.Dimensions > 0 {
		if err := r.createVectorIndex(ctx, m.ID, m.Vector.Dimensions); err != nil {
			_, _ = r.q.DeleteMemoryStore(ctx, m.ID)
			return err
		}
	}

	// Provision the backing table for a document store. Documents are opaque
	// JSON objects, so the table is a fixed (id, data, timestamps) shape rather
	// than the declared columns — reads use JSON functions to reach into `data`.
	// Roll the row back on failure so a store never lacks its table.
	if m.Type == models.MemoryDocument && m.Document != nil {
		if err := r.createDocumentTable(ctx, m.Document.Table); err != nil {
			_, _ = r.q.DeleteMemoryStore(ctx, m.ID)
			return err
		}
	}
	return nil
}

// RunReadQuery executes an already-validated document-store read query and
// returns the matched rows as generic maps (column name -> value). It trusts
// models.ValidateReadSQL to have confined the query to store.Document.Table;
// as defense in depth it runs inside a transaction that is always rolled back,
// so even a query that somehow slipped a mutation past the gate cannot persist.
//
// `store` selects which database to read: today every store lives in this one
// sqlite database, but resolving it here (rather than in the caller) keeps
// per-store / cross-database routing a change localized to this method.
func (r *memoryRepo) RunReadQuery(ctx context.Context, store *models.MemoryStore, query string) ([]map[string]any, error) {
	if store == nil || store.Type != models.MemoryDocument || store.Document == nil {
		return nil, fmt.Errorf("sqlite: read query requires a document store")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: begin read: %w", err)
	}
	// A read never commits — rolling back guarantees the query leaves no trace.
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("sqlite: run read query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("sqlite: read columns: %w", err)
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("sqlite: scan read row: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			// sqlite hands text/blob back as []byte; surface text as a string so
			// the row marshals to friendly JSON for the flow's next node.
			if b, ok := cells[i].([]byte); ok {
				row[name] = string(b)
			} else {
				row[name] = cells[i]
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate read rows: %w", err)
	}
	return out, nil
}

func (r *memoryRepo) GetByID(ctx context.Context, id string) (*models.MemoryStore, error) {
	row, err := r.q.GetMemoryStore(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return memoryFromRow(row)
}

func (r *memoryRepo) List(ctx context.Context) ([]models.MemoryStore, error) {
	rows, err := r.q.ListMemoryStores(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]models.MemoryStore, 0, len(rows))
	for _, row := range rows {
		rec, err := memoryFromRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, *rec)
	}
	return items, nil
}

func (r *memoryRepo) Delete(ctx context.Context, id string) error {
	// Fetch first so we know whether to drop a companion vector index.
	store, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if _, err := r.q.DeleteMemoryStore(ctx, id); err != nil {
		return err
	}
	if store.Type == models.MemoryVector {
		if err := r.dropVectorIndex(ctx, id); err != nil {
			// The store row is gone; a dangling index is a soft failure.
			fmt.Printf("sqlite: warning: dropping vector index for %s: %v\n", id, err)
		}
	}
	if store.Type == models.MemoryDocument && store.Document != nil {
		if err := r.dropDocumentTable(ctx, store.Document.Table); err != nil {
			// The store row is gone; a dangling table is a soft failure.
			fmt.Printf("sqlite: warning: dropping document table for %s: %v\n", id, err)
		}
	}
	return nil
}

// createDocumentTable provisions the (id, data, timestamps) table backing a
// document store. Table DDL cannot be parameterized, so the name is validated
// as a plain identifier first — never interpolated raw.
func (r *memoryRepo) createDocumentTable(ctx context.Context, table string) error {
	if !models.IsSafeIdentifier(table) {
		return fmt.Errorf("sqlite: document store table %q is not a valid identifier", table)
	}
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`, table,
	)
	if _, err := r.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("sqlite: create document table: %w", err)
	}
	return nil
}

func (r *memoryRepo) dropDocumentTable(ctx context.Context, table string) error {
	if !models.IsSafeIdentifier(table) {
		return fmt.Errorf("sqlite: document store table %q is not a valid identifier", table)
	}
	_, err := r.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
	return err
}

// WriteDocument inserts one JSON document into the store's table. The table name
// is validated (never user-controlled SQL) and the document is serialized and
// bound as a single parameter, so there is no injection surface and the payload
// shape is never interpreted.
func (r *memoryRepo) WriteDocument(ctx context.Context, store *models.MemoryStore, doc map[string]any) (string, error) {
	if store == nil || store.Type != models.MemoryDocument || store.Document == nil {
		return "", fmt.Errorf("sqlite: write requires a document store")
	}
	table := store.Document.Table
	if !models.IsSafeIdentifier(table) {
		return "", fmt.Errorf("sqlite: document store table %q is not a valid identifier", table)
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("sqlite: marshal document: %w", err)
	}
	id := repository.NewID(repository.DocumentIDPrefix)
	now := nowMillis()
	stmt := fmt.Sprintf("INSERT INTO %s (id, data, created_at, updated_at) VALUES (?, ?, ?, ?)", table)
	if _, err := r.db.ExecContext(ctx, stmt, id, string(payload), now, now); err != nil {
		return "", fmt.Errorf("sqlite: write document: %w", err)
	}
	return id, nil
}

// createVectorIndex creates the vec0 virtual table that stores this store's
// embeddings. Table DDL cannot be parameterized, so the name is sanitized and
// the dimension is a validated integer.
func (r *memoryRepo) createVectorIndex(ctx context.Context, id string, dimensions int) error {
	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(embedding float[%d])",
		vecTableName(id), dimensions,
	)
	if _, err := r.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("sqlite: create vector index: %w", err)
	}
	return nil
}

func (r *memoryRepo) dropVectorIndex(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+vecTableName(id))
	return err
}

// vecTableName derives a safe vec0 table name from a store id.
func vecTableName(id string) string {
	var b strings.Builder
	b.WriteString("vec_")
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func memoryFromRow(row sqlcgen.MemoryStore) (*models.MemoryStore, error) {
	rec := &models.MemoryStore{
		ID:          row.ID,
		Name:        row.Name,
		Type:        models.MemoryType(row.Type),
		Description: row.Description,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.VectorConfig.Valid && row.VectorConfig.String != "" {
		var cfg models.VectorMemoryConfig
		if err := json.Unmarshal([]byte(row.VectorConfig.String), &cfg); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal vector config for %s: %w", row.ID, err)
		}
		rec.Vector = &cfg
	}
	if row.DocumentConfig.Valid && row.DocumentConfig.String != "" {
		var cfg models.DocumentMemoryConfig
		if err := json.Unmarshal([]byte(row.DocumentConfig.String), &cfg); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal document config for %s: %w", row.ID, err)
		}
		rec.Document = &cfg
	}
	return rec, nil
}
