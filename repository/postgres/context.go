package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

const (
	contextColumns = `id, title, context, header, updated_by_type, updated_by_address, created_at, updated_at`

	contextUpsertSQL = `
INSERT INTO contexts (id, title, context, header, updated_by_type, updated_by_address, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    context = EXCLUDED.context,
    header = EXCLUDED.header,
    updated_by_type = EXCLUDED.updated_by_type,
    updated_by_address = EXCLUDED.updated_by_address,
    updated_at = EXCLUDED.updated_at`

	contextSearchSQL = `($1::text = '' OR title ILIKE '%' || $1::text || '%')`
)

type contextRepo struct {
	db *sql.DB
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
			return fmt.Errorf("postgres: marshal header: %w", err)
		}
		header = string(b)
	}
	body := c.Context
	if body == "" {
		body = "{}"
	}
	_, err := r.db.ExecContext(ctx, contextUpsertSQL,
		c.ID, c.Title, body, header, string(c.UpdatedBy.By), c.UpdatedBy.Address, c.CreatedAt, c.UpdatedAt)
	return err
}

func (r *contextRepo) GetByID(ctx context.Context, id string) (*models.ContextRecord, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+contextColumns+` FROM contexts WHERE id = $1`, id)
	return scanContext(row)
}

func (r *contextRepo) List(ctx context.Context, p repository.ListParams) ([]models.ContextRecord, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM contexts WHERE `+contextSearchSQL, p.Search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+contextColumns+` FROM contexts WHERE `+contextSearchSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3`,
		p.Search, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]models.ContextRecord, 0, limit)
	for rows.Next() {
		rec, err := scanContext(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *contextRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM contexts WHERE id = $1`, id)
}

func scanContext(row scanner) (*models.ContextRecord, error) {
	var (
		rec              models.ContextRecord
		header, by, addr string
	)
	if err := row.Scan(&rec.ID, &rec.Title, &rec.Context, &header, &by, &addr, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	rec.UpdatedBy = models.LastChange{By: models.ContextChangeType(by), Address: addr}
	if header != "" {
		if err := json.Unmarshal([]byte(header), &rec.Header); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal header for %s: %w", rec.ID, err)
		}
	}
	return &rec, nil
}
