package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

const (
	connectColumns = `id, label, base_url, token, admin_token, kind, is_default, created_at, updated_at`

	connectUpsertSQL = `
INSERT INTO connect_connections (
    id, label, base_url, token, admin_token, kind, is_default, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    label = EXCLUDED.label,
    base_url = EXCLUDED.base_url,
    token = EXCLUDED.token,
    admin_token = EXCLUDED.admin_token,
    kind = EXCLUDED.kind,
    is_default = EXCLUDED.is_default,
    updated_at = EXCLUDED.updated_at`

	// Default first, then newest — so the first row is the one calls route
	// through and Default() is a read of the head of this list.
	connectListSQL = `SELECT ` + connectColumns + ` FROM connect_connections
	 ORDER BY is_default DESC, updated_at DESC, id DESC`
)

type connectRepo struct {
	db *sql.DB
}

func (r *connectRepo) Upsert(ctx context.Context, c *models.ConnectConnection) error {
	now := nowMillis()
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM connect_connections`).Scan(&total); err != nil {
		return err
	}
	if c.ID == "" {
		c.ID = repository.NewID(repository.ConnectIDPrefix)
		if c.CreatedAt == 0 {
			c.CreatedAt = now
		}
		// The very first connection is the default — a lone gateway is always the
		// one calls route through.
		if total == 0 {
			c.IsDefault = true
		}
	} else if existing, err := r.GetByID(ctx, c.ID); err == nil {
		c.CreatedAt = existing.CreatedAt
		// An empty token on update keeps the stored secret, so a rename or a
		// default flip never has to re-send it (protects every caller, not just
		// the HTTP controller). Each token is preserved independently.
		if c.Token == "" {
			c.Token = existing.Token
		}
		if c.AdminToken == "" {
			c.AdminToken = existing.AdminToken
		}
	} else if errors.Is(err, repository.ErrNotFound) {
		if c.CreatedAt == 0 {
			c.CreatedAt = now
		}
		if total == 0 {
			c.IsDefault = true
		}
	} else {
		return err
	}
	c.UpdatedAt = now

	// Saving a connection as default demotes every other one first, so the flag
	// stays single-valued.
	if c.IsDefault {
		if err := r.clearDefault(ctx); err != nil {
			return err
		}
	}
	_, err := r.db.ExecContext(ctx, connectUpsertSQL,
		c.ID, c.Label, c.BaseURL, c.Token, c.AdminToken, c.Kind, c.IsDefault, c.CreatedAt, c.UpdatedAt)
	return err
}

func (r *connectRepo) GetByID(ctx context.Context, id string) (*models.ConnectConnection, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+connectColumns+` FROM connect_connections WHERE id = $1`, id)
	return scanConnect(row)
}

func (r *connectRepo) List(ctx context.Context) ([]models.ConnectConnection, error) {
	rows, err := r.db.QueryContext(ctx, connectListSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]models.ConnectConnection, 0, 4)
	for rows.Next() {
		rec, err := scanConnect(rows)
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

// Default returns the connection marked default. The list is ordered
// default-first, so the answer is its head — and a head that is not flagged
// means no row is, which is ErrNotFound rather than a silent fallback.
func (r *connectRepo) Default(ctx context.Context) (*models.ConnectConnection, error) {
	items, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 || !items[0].IsDefault {
		return nil, repository.ErrNotFound
	}
	first := items[0]
	return &first, nil
}

func (r *connectRepo) Delete(ctx context.Context, id string) error {
	prev, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := execExpectRow(ctx, r.db, `DELETE FROM connect_connections WHERE id = $1`, id); err != nil {
		return err
	}
	// Removing the default promotes the newest remaining connection so a default
	// always exists while any connection does.
	if prev.IsDefault {
		items, err := r.List(ctx)
		if err != nil {
			return err
		}
		if len(items) > 0 {
			return r.setDefault(ctx, items[0].ID)
		}
	}
	return nil
}

func (r *connectRepo) SetDefault(ctx context.Context, id string) error {
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}
	return r.setDefault(ctx, id)
}

func (r *connectRepo) setDefault(ctx context.Context, id string) error {
	if err := r.clearDefault(ctx); err != nil {
		return err
	}
	return execExpectRow(ctx, r.db,
		`UPDATE connect_connections SET is_default = TRUE, updated_at = $1 WHERE id = $2`,
		nowMillis(), id)
}

func (r *connectRepo) clearDefault(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `UPDATE connect_connections SET is_default = FALSE WHERE is_default`)
	return err
}

func scanConnect(row scanner) (*models.ConnectConnection, error) {
	var rec models.ConnectConnection
	if err := row.Scan(&rec.ID, &rec.Label, &rec.BaseURL, &rec.Token, &rec.AdminToken,
		&rec.Kind, &rec.IsDefault, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	return &rec, nil
}
