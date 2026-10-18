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
	nodeSettingColumns = `id, node_uniq_id, node_type, title, settings, created_at, updated_at`

	nodeSettingUpsertSQL = `
INSERT INTO node_settings (id, node_uniq_id, node_type, title, settings, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    node_uniq_id = EXCLUDED.node_uniq_id,
    node_type = EXCLUDED.node_type,
    title = EXCLUDED.title,
    settings = EXCLUDED.settings,
    updated_at = EXCLUDED.updated_at`

	// $1 search, $2 node filter — the node drawer fetches just one node's profiles.
	nodeSettingFilterSQL = `($1::text = '' OR title ILIKE '%' || $1::text || '%')
	  AND ($2::text = '' OR node_uniq_id = $2::text)`
)

type nodeSettingRepo struct {
	db *sql.DB
}

func (r *nodeSettingRepo) Upsert(ctx context.Context, s *models.NodeSetting) error {
	now := nowMillis()
	if s.ID == "" {
		s.ID = repository.NewID(repository.NodeSettingIDPrefix)
		if s.CreatedAt == 0 {
			s.CreatedAt = now
		}
	} else if existing, err := r.GetByID(ctx, s.ID); err == nil {
		s.CreatedAt = existing.CreatedAt
	} else if errors.Is(err, repository.ErrNotFound) {
		if s.CreatedAt == 0 {
			s.CreatedAt = now
		}
	} else {
		return err
	}
	s.UpdatedAt = now

	if s.Settings == nil {
		s.Settings = map[string]any{}
	}
	settings, err := json.Marshal(s.Settings)
	if err != nil {
		return fmt.Errorf("postgres: marshal node settings: %w", err)
	}
	_, err = r.db.ExecContext(ctx, nodeSettingUpsertSQL,
		s.ID, s.NodeUniqID, s.NodeType, s.Title, string(settings), s.CreatedAt, s.UpdatedAt)
	return err
}

func (r *nodeSettingRepo) GetByID(ctx context.Context, id string) (*models.NodeSetting, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+nodeSettingColumns+` FROM node_settings WHERE id = $1`, id)
	return scanNodeSetting(row)
}

func (r *nodeSettingRepo) List(ctx context.Context, p repository.ListParams) ([]models.NodeSetting, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_settings WHERE `+nodeSettingFilterSQL,
		p.Search, p.NodeUniqID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+nodeSettingColumns+` FROM node_settings WHERE `+nodeSettingFilterSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $3 OFFSET $4`,
		p.Search, p.NodeUniqID, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]models.NodeSetting, 0, limit)
	for rows.Next() {
		rec, err := scanNodeSetting(rows)
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

func (r *nodeSettingRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM node_settings WHERE id = $1`, id)
}

func scanNodeSetting(row scanner) (*models.NodeSetting, error) {
	rec := &models.NodeSetting{Settings: map[string]any{}}
	var settings string
	if err := row.Scan(&rec.ID, &rec.NodeUniqID, &rec.NodeType, &rec.Title, &settings,
		&rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	if settings != "" {
		if err := json.Unmarshal([]byte(settings), &rec.Settings); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal node settings for %s: %w", rec.ID, err)
		}
	}
	return rec, nil
}
