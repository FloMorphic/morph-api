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
	return nil
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
	return nil
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
