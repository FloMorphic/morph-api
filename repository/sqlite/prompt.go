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

type promptRepo struct {
	q *sqlcgen.Queries
}

func (r *promptRepo) Upsert(ctx context.Context, p *models.PromptRecord) error {
	now := nowMillis()
	if p.ID == "" {
		p.ID = repository.NewID(repository.PromptIDPrefix)
		if p.CreatedAt == 0 {
			p.CreatedAt = now
		}
	} else if existing, err := r.GetByID(ctx, p.ID); err == nil {
		p.CreatedAt = existing.CreatedAt
	} else if errors.Is(err, repository.ErrNotFound) {
		if p.CreatedAt == 0 {
			p.CreatedAt = now
		}
	} else {
		return err
	}
	p.UpdatedAt = now

	if p.Variables == nil {
		p.Variables = []models.PromptVariable{}
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	variables, err := json.Marshal(p.Variables)
	if err != nil {
		return fmt.Errorf("sqlite: marshal prompt variables: %w", err)
	}
	tags, err := json.Marshal(p.Tags)
	if err != nil {
		return fmt.Errorf("sqlite: marshal prompt tags: %w", err)
	}
	return r.q.UpsertPrompt(ctx, sqlcgen.UpsertPromptParams{
		ID:          p.ID,
		Title:       p.Title,
		Description: p.Description,
		Template:    p.Template,
		Variables:   string(variables),
		Tags:        string(tags),
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	})
}

func (r *promptRepo) GetByID(ctx context.Context, id string) (*models.PromptRecord, error) {
	row, err := r.q.GetPrompt(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return promptFromRow(row)
}

func (r *promptRepo) List(ctx context.Context, p repository.ListParams) ([]models.PromptRecord, int64, error) {
	limit := clampLimit(p.Limit)
	total, err := r.q.CountPrompts(ctx, p.Search)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListPrompts(ctx, sqlcgen.ListPromptsParams{
		Search: p.Search,
		Offset: int64(p.Offset),
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.PromptRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := promptFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *promptRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeletePrompt(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func promptFromRow(row sqlcgen.Prompt) (*models.PromptRecord, error) {
	rec := &models.PromptRecord{
		ID:          row.ID,
		Title:       row.Title,
		Description: row.Description,
		Template:    row.Template,
		Variables:   []models.PromptVariable{},
		Tags:        []string{},
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.Variables != "" {
		if err := json.Unmarshal([]byte(row.Variables), &rec.Variables); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal prompt variables for %s: %w", row.ID, err)
		}
	}
	if row.Tags != "" {
		if err := json.Unmarshal([]byte(row.Tags), &rec.Tags); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal prompt tags for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}
