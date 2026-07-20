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

type nodeSettingRepo struct {
	q *sqlcgen.Queries
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
		return fmt.Errorf("sqlite: marshal node settings: %w", err)
	}
	return r.q.UpsertNodeSetting(ctx, sqlcgen.UpsertNodeSettingParams{
		ID:         s.ID,
		NodeUniqID: s.NodeUniqID,
		NodeType:   s.NodeType,
		Title:      s.Title,
		Settings:   string(settings),
		CreatedAt:  s.CreatedAt,
		UpdatedAt:  s.UpdatedAt,
	})
}

func (r *nodeSettingRepo) GetByID(ctx context.Context, id string) (*models.NodeSetting, error) {
	row, err := r.q.GetNodeSetting(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return nodeSettingFromRow(row)
}

func (r *nodeSettingRepo) List(ctx context.Context, p repository.ListParams) ([]models.NodeSetting, int64, error) {
	limit := clampLimit(p.Limit)
	total, err := r.q.CountNodeSettings(ctx, sqlcgen.CountNodeSettingsParams{
		Search:     p.Search,
		NodeUniqID: p.NodeUniqID,
	})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListNodeSettings(ctx, sqlcgen.ListNodeSettingsParams{
		Search:     p.Search,
		NodeUniqID: p.NodeUniqID,
		Offset:     int64(p.Offset),
		Limit:      int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.NodeSetting, 0, len(rows))
	for _, row := range rows {
		rec, err := nodeSettingFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *nodeSettingRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteNodeSetting(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func nodeSettingFromRow(row sqlcgen.NodeSetting) (*models.NodeSetting, error) {
	rec := &models.NodeSetting{
		ID:         row.ID,
		NodeUniqID: row.NodeUniqID,
		NodeType:   row.NodeType,
		Title:      row.Title,
		Settings:   map[string]any{},
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
	if row.Settings != "" {
		if err := json.Unmarshal([]byte(row.Settings), &rec.Settings); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal node settings for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}
