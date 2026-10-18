package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/sqlite"
)

const memoryColumns = `id, name, type, description, vector_config, document_config, created_at, updated_at`

// memoryRepo keeps the store rows and the document tables in postgres, and
// hands every vector operation to the sqlite-vec sidecar — see the package
// comment: embeddings stay in sqlite even on a postgres deployment, so there is
// no pgvector requirement and a store created before the switch is still
// searchable afterwards.
type memoryRepo struct {
	db *sql.DB
	// vec is nil when no sidecar could be opened (or it was switched off). Every
	// vector method then fails with an explanation rather than half-working.
	vec *sqlite.VectorIndex
}

// errNoVectorIndex explains the one thing a postgres deployment can lack.
func (r *memoryRepo) vectorIndex() (*sqlite.VectorIndex, error) {
	if r.vec == nil {
		return nil, fmt.Errorf("postgres: vector memory needs a sqlite vector index; " +
			"set DB_VECTOR_SOURCE to a writable sqlite file path")
	}
	return r.vec, nil
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

	// A vector store with no reachable index must fail before the row exists,
	// not after: a store the app cannot index or search is worse than no store.
	var vec *sqlite.VectorIndex
	if m.Type == models.MemoryVector {
		var err error
		if vec, err = r.vectorIndex(); err != nil {
			return err
		}
	}

	var vecCfg, docCfg sql.NullString
	switch m.Type {
	case models.MemoryVector:
		if m.Vector != nil {
			b, err := json.Marshal(m.Vector)
			if err != nil {
				return fmt.Errorf("postgres: marshal vector config: %w", err)
			}
			vecCfg = sql.NullString{String: string(b), Valid: true}
		}
	case models.MemoryDocument:
		if m.Document != nil {
			b, err := json.Marshal(m.Document)
			if err != nil {
				return fmt.Errorf("postgres: marshal document config: %w", err)
			}
			docCfg = sql.NullString{String: string(b), Valid: true}
		}
	}

	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO memory_stores (id, name, type, description, vector_config, document_config, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		m.ID, m.Name, string(m.Type), m.Description, vecCfg, docCfg, m.CreatedAt, m.UpdatedAt); err != nil {
		return err
	}

	// Provision the backing index/table, rolling the row back on failure so a
	// store never exists without the thing that makes it usable.
	if m.Type == models.MemoryVector && m.Vector != nil && m.Vector.Dimensions > 0 {
		if err := vec.Provision(ctx, m.ID, m.Vector); err != nil {
			_ = r.deleteRow(ctx, m.ID)
			return err
		}
	}
	if m.Type == models.MemoryDocument && m.Document != nil {
		if err := r.createDocumentTable(ctx, m.Document.Table); err != nil {
			_ = r.deleteRow(ctx, m.ID)
			return err
		}
	}
	return nil
}

func (r *memoryRepo) GetByID(ctx context.Context, id string) (*models.MemoryStore, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+memoryColumns+` FROM memory_stores WHERE id = $1`, id)
	return scanMemoryStore(row)
}

func (r *memoryRepo) List(ctx context.Context) ([]models.MemoryStore, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+memoryColumns+` FROM memory_stores ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]models.MemoryStore, 0, 8)
	for rows.Next() {
		rec, err := scanMemoryStore(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *memoryRepo) Delete(ctx context.Context, id string) error {
	// Fetch first so we know what companion object to drop.
	store, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := r.deleteRow(ctx, id); err != nil {
		return err
	}
	if store.Type == models.MemoryVector && r.vec != nil {
		if err := r.vec.Drop(ctx, id); err != nil {
			// The store row is gone; a dangling index is a soft failure.
			fmt.Printf("postgres: warning: dropping vector index for %s: %v\n", id, err)
		}
	}
	if store.Type == models.MemoryDocument && store.Document != nil {
		if err := r.dropDocumentTable(ctx, store.Document.Table); err != nil {
			// The store row is gone; a dangling table is a soft failure.
			fmt.Printf("postgres: warning: dropping document table for %s: %v\n", id, err)
		}
	}
	return nil
}

func (r *memoryRepo) deleteRow(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM memory_stores WHERE id = $1`, id)
	return err
}

// createDocumentTable provisions the (id, data, timestamps) table backing a
// document store. Table DDL cannot be parameterized, so the name is validated
// as a plain identifier first — never interpolated raw. `data` is JSONB rather
// than text because these rows are what a workflow's own read queries select
// from, and postgres's JSON operators are the reason to be on postgres at all.
func (r *memoryRepo) createDocumentTable(ctx context.Context, table string) error {
	if !models.IsSafeIdentifier(table) {
		return fmt.Errorf("postgres: document store table %q is not a valid identifier", table)
	}
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			data JSONB NOT NULL,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)`, table,
	)
	if _, err := r.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("postgres: create document table: %w", err)
	}
	return nil
}

func (r *memoryRepo) dropDocumentTable(ctx context.Context, table string) error {
	if !models.IsSafeIdentifier(table) {
		return fmt.Errorf("postgres: document store table %q is not a valid identifier", table)
	}
	_, err := r.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
	return err
}

// RunReadQuery executes an already-validated document-store read query and
// returns the matched rows as generic maps (column name -> value). It trusts
// models.ValidateReadSQL to have confined the query to a single read-only
// statement; as defense in depth it runs inside a READ ONLY transaction that is
// always rolled back, so even a query that somehow slipped a mutation past the
// gate cannot persist — on postgres the server itself refuses the write.
//
// The SQL is the query author's, so it is written in the dialect of the database
// the deployment runs: a document read authored for sqlite (`json_extract`) has
// to be rewritten in postgres's JSON operators (`data->>'field'`) after a switch.
func (r *memoryRepo) RunReadQuery(ctx context.Context, store *models.MemoryStore, query string) ([]map[string]any, error) {
	if store == nil || store.Type != models.MemoryDocument || store.Document == nil {
		return nil, fmt.Errorf("postgres: read query requires a document store")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("postgres: begin read: %w", err)
	}
	// A read never commits — rolling back guarantees the query leaves no trace.
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres: run read query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("postgres: read columns: %w", err)
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("postgres: scan read row: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			switch v := cells[i].(type) {
			case []byte:
				// json/jsonb and text come back as bytes; surface them as strings so
				// the row marshals to friendly JSON for the flow's next node.
				row[name] = string(v)
			case time.Time:
				// A timestamp column has no counterpart in the sqlite backend (which
				// stores epoch millis); render it as RFC3339 so it survives JSON.
				row[name] = v.Format(time.RFC3339Nano)
			default:
				row[name] = cells[i]
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate read rows: %w", err)
	}
	return out, nil
}

// WriteDocument inserts one JSON document into the store's table. The table name
// is validated (never user-controlled SQL) and the document is serialized and
// bound as a single parameter cast to jsonb, so there is no injection surface
// and the payload's shape is never interpreted.
func (r *memoryRepo) WriteDocument(ctx context.Context, store *models.MemoryStore, doc map[string]any) (string, error) {
	table, err := documentTable(store, "write")
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("postgres: marshal document: %w", err)
	}
	id := repository.NewID(repository.DocumentIDPrefix)
	now := nowMillis()
	stmt := fmt.Sprintf(
		"INSERT INTO %s (id, data, created_at, updated_at) VALUES ($1, $2::jsonb, $3, $4)", table)
	if _, err := r.db.ExecContext(ctx, stmt, id, string(payload), now, now); err != nil {
		return "", fmt.Errorf("postgres: write document: %w", err)
	}
	return id, nil
}

// UpdateDocument replaces the JSON document stored under id, bumping updated_at.
// A zero rows-affected means no document had that id — repository.ErrNotFound.
func (r *memoryRepo) UpdateDocument(ctx context.Context, store *models.MemoryStore, id string, doc map[string]any) error {
	table, err := documentTable(store, "update")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("postgres: marshal document: %w", err)
	}
	stmt := fmt.Sprintf("UPDATE %s SET data = $1::jsonb, updated_at = $2 WHERE id = $3", table)
	if err := execExpectRow(ctx, r.db, stmt, string(payload), nowMillis(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return err
		}
		return fmt.Errorf("postgres: update document: %w", err)
	}
	return nil
}

// DeleteDocument removes the document with the given id from the store's table.
func (r *memoryRepo) DeleteDocument(ctx context.Context, store *models.MemoryStore, id string) error {
	table, err := documentTable(store, "delete")
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf("DELETE FROM %s WHERE id = $1", table)
	if err := execExpectRow(ctx, r.db, stmt, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return err
		}
		return fmt.Errorf("postgres: delete document: %w", err)
	}
	return nil
}

// documentTable resolves and validates the table one document operation targets.
func documentTable(store *models.MemoryStore, op string) (string, error) {
	if store == nil || store.Type != models.MemoryDocument || store.Document == nil {
		return "", fmt.Errorf("postgres: %s requires a document store", op)
	}
	table := store.Document.Table
	if !models.IsSafeIdentifier(table) {
		return "", fmt.Errorf("postgres: document store table %q is not a valid identifier", table)
	}
	return table, nil
}

// The vector operations live in the sqlite-vec sidecar, so they are forwarded
// verbatim — identical behaviour, identical limits, whichever backend holds the
// store rows.

func (r *memoryRepo) IndexVector(ctx context.Context, store *models.MemoryStore, content string, vector []float32, metadata map[string]any, partition string) (string, error) {
	vec, err := r.vectorIndex()
	if err != nil {
		return "", err
	}
	return vec.Index(ctx, store, content, vector, metadata, partition)
}

func (r *memoryRepo) SearchVectors(ctx context.Context, store *models.MemoryStore, vector []float32, k int, partition string, minScore float64, filter map[string]any) ([]models.VectorMatch, error) {
	vec, err := r.vectorIndex()
	if err != nil {
		return nil, err
	}
	return vec.Search(ctx, store, vector, k, partition, minScore, filter)
}

func (r *memoryRepo) DeleteVector(ctx context.Context, store *models.MemoryStore, docID string) error {
	vec, err := r.vectorIndex()
	if err != nil {
		return err
	}
	return vec.DeleteRecord(ctx, store, docID)
}

func scanMemoryStore(row scanner) (*models.MemoryStore, error) {
	var (
		rec            models.MemoryStore
		typ            string
		vecCfg, docCfg sql.NullString
	)
	if err := row.Scan(&rec.ID, &rec.Name, &typ, &rec.Description, &vecCfg, &docCfg,
		&rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	rec.Type = models.MemoryType(typ)
	if vecCfg.Valid && vecCfg.String != "" {
		var cfg models.VectorMemoryConfig
		if err := json.Unmarshal([]byte(vecCfg.String), &cfg); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal vector config for %s: %w", rec.ID, err)
		}
		rec.Vector = &cfg
	}
	if docCfg.Valid && docCfg.String != "" {
		var cfg models.DocumentMemoryConfig
		if err := json.Unmarshal([]byte(docCfg.String), &cfg); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal document config for %s: %w", rec.ID, err)
		}
		rec.Document = &cfg
	}
	return &rec, nil
}
