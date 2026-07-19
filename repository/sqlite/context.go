package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/sqlite/sqlcgen"
)

type contextRepo struct {
	q *sqlcgen.Queries
}

func (r *contextRepo) Upsert(ctx context.Context, c *models.ContextRecord) error {
	now := nowMillis()
	if c.ID == "" {
		c.ID = repository.NewID(repository.ContextIDPrefix)
		if c.CreatedAt == 0 {
			c.CreatedAt = now
		}
	} else if existing, err := r.GetByID(ctx, c.ID); err == nil {
		c.CreatedAt = existing.CreatedAt
	} else if errors.Is(err, repository.ErrNotFound) {
		if c.CreatedAt == 0 {
			c.CreatedAt = now
		}
	} else {
		return err
	}
	c.UpdatedAt = now

	if c.UpdatedBy.By == "" {
		c.UpdatedBy.By = models.ByAPI
	}
	header := "{}"
	if c.Header != nil {
		b, err := json.Marshal(c.Header)
		if err != nil {
			return fmt.Errorf("sqlite: marshal header: %w", err)
		}
		header = string(b)
	}
	body := c.Context
	if body == "" {
		body = "{}"
	}
	return r.q.UpsertContext(ctx, sqlcgen.UpsertContextParams{
		ID:               c.ID,
		Title:            c.Title,
		Context:          body,
		Header:           header,
		UpdatedByType:    string(c.UpdatedBy.By),
		UpdatedByAddress: c.UpdatedBy.Address,
		CreatedAt:        c.CreatedAt,
		UpdatedAt:        c.UpdatedAt,
	})
}

func (r *contextRepo) GetByID(ctx context.Context, id string) (*models.ContextRecord, error) {
	row, err := r.q.GetContext(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return contextFromRow(row)
}

func (r *contextRepo) List(ctx context.Context, p repository.ListParams) ([]models.ContextRecord, int64, error) {
	limit := clampLimit(p.Limit)
	total, err := r.q.CountContexts(ctx, p.Search)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListContexts(ctx, sqlcgen.ListContextsParams{
		Search: p.Search,
		Offset: int64(p.Offset),
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.ContextRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := contextFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *contextRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteContext(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func contextFromRow(row sqlcgen.Context) (*models.ContextRecord, error) {
	rec := &models.ContextRecord{
		ID:        row.ID,
		Title:     row.Title,
		Context:   row.Context,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		UpdatedBy: models.LastChange{
			By:      models.ContextChangeType(row.UpdatedByType),
			Address: row.UpdatedByAddress,
		},
	}
	if row.Header != "" {
		if err := json.Unmarshal([]byte(row.Header), &rec.Header); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal header for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}
