package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/FloMorphic/morph-api/repository/sqlite/sqlcgen"
)

type connectRepo struct {
	q *sqlcgen.Queries
}

func (r *connectRepo) Upsert(ctx context.Context, c *models.ConnectConnection) error {
	now := nowMillis()
	total, err := r.q.CountConnectConnections(ctx)
	if err != nil {
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
		if err := r.q.ClearConnectDefault(ctx); err != nil {
			return err
		}
	}
	return r.q.UpsertConnectConnection(ctx, sqlcgen.UpsertConnectConnectionParams{
		ID:         c.ID,
		Label:      c.Label,
		BaseUrl:    c.BaseURL,
		Token:      c.Token,
		AdminToken: c.AdminToken,
		Kind:       c.Kind,
		IsDefault:  boolToInt(c.IsDefault),
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	})
}

func (r *connectRepo) GetByID(ctx context.Context, id string) (*models.ConnectConnection, error) {
	row, err := r.q.GetConnectConnection(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return connectFromRow(row), nil
}

func (r *connectRepo) List(ctx context.Context) ([]models.ConnectConnection, error) {
	rows, err := r.q.ListConnectConnections(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]models.ConnectConnection, 0, len(rows))
	for _, row := range rows {
		items = append(items, *connectFromRow(row))
	}
	return items, nil
}

func (r *connectRepo) Default(ctx context.Context) (*models.ConnectConnection, error) {
	rows, err := r.q.ListConnectConnections(ctx) // ordered is_default DESC first
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, repository.ErrNotFound
	}
	first := connectFromRow(rows[0])
	if !first.IsDefault {
		// No row flagged default (e.g. all demoted); fall back to the newest.
		return nil, repository.ErrNotFound
	}
	return first, nil
}

func (r *connectRepo) Delete(ctx context.Context, id string) error {
	prev, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	n, err := r.q.DeleteConnectConnection(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	// Removing the default promotes the newest remaining connection so a default
	// always exists while any connection does.
	if prev.IsDefault {
		rows, err := r.q.ListConnectConnections(ctx)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			return r.setDefault(ctx, rows[0].ID)
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
	if err := r.q.ClearConnectDefault(ctx); err != nil {
		return err
	}
	n, err := r.q.SetConnectDefault(ctx, sqlcgen.SetConnectDefaultParams{
		UpdatedAt: nowMillis(),
		ID:        id,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func connectFromRow(row sqlcgen.ConnectConnection) *models.ConnectConnection {
	return &models.ConnectConnection{
		ID:         row.ID,
		Label:      row.Label,
		BaseURL:    row.BaseUrl,
		Token:      row.Token,
		AdminToken: row.AdminToken,
		Kind:       row.Kind,
		IsDefault:  row.IsDefault != 0,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}
