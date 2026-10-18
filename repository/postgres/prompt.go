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
	promptColumns = `id, title, description, template, variables, tags, created_at, updated_at`

	promptUpsertSQL = `
INSERT INTO prompts (id, title, description, template, variables, tags, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    description = EXCLUDED.description,
    template = EXCLUDED.template,
    variables = EXCLUDED.variables,
    tags = EXCLUDED.tags,
    updated_at = EXCLUDED.updated_at`

	promptSearchSQL = `($1::text = '' OR title ILIKE '%' || $1::text || '%' OR description ILIKE '%' || $1::text || '%')`
)

type promptRepo struct {
	db *sql.DB
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
		return fmt.Errorf("postgres: marshal prompt variables: %w", err)
	}
	tags, err := json.Marshal(p.Tags)
	if err != nil {
		return fmt.Errorf("postgres: marshal prompt tags: %w", err)
	}
	_, err = r.db.ExecContext(ctx, promptUpsertSQL,
		p.ID, p.Title, p.Description, p.Template, string(variables), string(tags), p.CreatedAt, p.UpdatedAt)
	return err
}

func (r *promptRepo) GetByID(ctx context.Context, id string) (*models.PromptRecord, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+promptColumns+` FROM prompts WHERE id = $1`, id)
	return scanPrompt(row)
}

func (r *promptRepo) List(ctx context.Context, p repository.ListParams) ([]models.PromptRecord, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM prompts WHERE `+promptSearchSQL, p.Search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+promptColumns+` FROM prompts WHERE `+promptSearchSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3`,
		p.Search, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]models.PromptRecord, 0, limit)
	for rows.Next() {
		rec, err := scanPrompt(rows)
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

// SeedPrompts inserts each seed prompt whose id is not already stored, keyed by
// that fixed id, so restarts don't duplicate them and later edits survive.
func (r *promptRepo) SeedPrompts(ctx context.Context, defs []models.PromptRecord) (int, error) {
	inserted := 0
	for i := range defs {
		def := defs[i]
		if def.ID == "" {
			continue // without a fixed id there is nothing to recognise on restart
		}
		if _, err := r.GetByID(ctx, def.ID); err == nil {
			continue // already seeded
		} else if !errors.Is(err, repository.ErrNotFound) {
			return inserted, err
		}
		if err := r.Upsert(ctx, &def); err != nil {
			return inserted, err
		}
		inserted++
	}
	return inserted, nil
}

func (r *promptRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM prompts WHERE id = $1`, id)
}

func scanPrompt(row scanner) (*models.PromptRecord, error) {
	rec := &models.PromptRecord{
		Variables: []models.PromptVariable{},
		Tags:      []string{},
	}
	var variables, tags string
	if err := row.Scan(&rec.ID, &rec.Title, &rec.Description, &rec.Template, &variables, &tags,
		&rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	if variables != "" {
		if err := json.Unmarshal([]byte(variables), &rec.Variables); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal prompt variables for %s: %w", rec.ID, err)
		}
	}
	if tags != "" {
		if err := json.Unmarshal([]byte(tags), &rec.Tags); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal prompt tags for %s: %w", rec.ID, err)
		}
	}
	return rec, nil
}
