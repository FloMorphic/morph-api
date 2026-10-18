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

// The upsert/list/count statements follow one shape across every entity here:
//
//   - an INSERT … ON CONFLICT (id) DO UPDATE that leaves created_at alone, so a
//     save never rewrites when a row first appeared;
//   - a search predicate of the form `($1 = ” OR col ILIKE '%' || $1 || '%')`,
//     where the parameter is cast to text explicitly — postgres cannot infer a
//     placeholder's type from a comparison with an untyped literal, and ILIKE
//     matches sqlite's case-insensitive LIKE;
//   - `ORDER BY updated_at DESC, id DESC` paging, matching the sqlite queries so
//     a page holds the same rows on either backend.
const (
	workflowColumns = `id, title, view_flow, created_at, updated_at`

	workflowUpsertSQL = `
INSERT INTO workflows (id, title, view_flow, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    view_flow = EXCLUDED.view_flow,
    updated_at = EXCLUDED.updated_at`

	workflowSearchSQL = `($1::text = '' OR title ILIKE '%' || $1::text || '%')`
)

type workflowRepo struct {
	db *sql.DB
}

func (r *workflowRepo) Upsert(ctx context.Context, w *models.FlowRecord) error {
	now := nowMillis()
	if w.ID == "" {
		w.ID = repository.NewID(repository.WorkflowIDPrefix)
		if w.CreatedAt == 0 {
			w.CreatedAt = now
		}
	} else if existing, err := r.GetByID(ctx, w.ID); err == nil {
		// Update: keep the original creation time.
		w.CreatedAt = existing.CreatedAt
	} else if errors.Is(err, repository.ErrNotFound) {
		if w.CreatedAt == 0 {
			w.CreatedAt = now
		}
	} else {
		return err
	}
	w.UpdatedAt = now

	viewFlow, err := json.Marshal(w.ViewFlow)
	if err != nil {
		return fmt.Errorf("postgres: marshal view_flow: %w", err)
	}
	_, err = r.db.ExecContext(ctx, workflowUpsertSQL, w.ID, w.Title, string(viewFlow), w.CreatedAt, w.UpdatedAt)
	return err
}

func (r *workflowRepo) GetByID(ctx context.Context, id string) (*models.FlowRecord, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+workflowColumns+` FROM workflows WHERE id = $1`, id)
	return scanWorkflow(row)
}

func (r *workflowRepo) List(ctx context.Context, p repository.ListParams) ([]models.FlowRecord, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflows WHERE `+workflowSearchSQL, p.Search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+workflowColumns+` FROM workflows WHERE `+workflowSearchSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3`,
		p.Search, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]models.FlowRecord, 0, limit)
	for rows.Next() {
		rec, err := scanWorkflow(rows)
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

func (r *workflowRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM workflows WHERE id = $1`, id)
}

func scanWorkflow(row scanner) (*models.FlowRecord, error) {
	var (
		rec      models.FlowRecord
		viewFlow string
	)
	if err := row.Scan(&rec.ID, &rec.Title, &viewFlow, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	if viewFlow != "" {
		if err := json.Unmarshal([]byte(viewFlow), &rec.ViewFlow); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal view_flow for %s: %w", rec.ID, err)
		}
	}
	return &rec, nil
}

// scanner is the bit of *sql.Row and *sql.Rows a row-mapper needs, so one
// mapper serves both the single-row get and the list loop.
type scanner interface {
	Scan(dest ...any) error
}

// execExpectRow runs a statement that must match a row — a delete, or a
// targeted state update — and reports repository.ErrNotFound when it matched
// nothing, which is the contract every Delete in the repository layer has.
func execExpectRow(ctx context.Context, db *sql.DB, query string, args ...any) error {
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

// marshalObject renders a free-form object column, defaulting a nil map to "{}"
// so a NOT NULL text column always holds valid JSON.
func marshalObject(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalObject decodes a free-form object column, leaving the target nil for
// empty/`{}` text.
func unmarshalObject(s string, dst *map[string]any) error {
	if s == "" || s == "{}" {
		return nil
	}
	return json.Unmarshal([]byte(s), dst)
}
