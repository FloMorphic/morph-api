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
	workflowColumns = `id, title, view_flow, tags, created_at, updated_at`

	workflowUpsertSQL = `
INSERT INTO workflows (id, title, view_flow, tags, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    view_flow = EXCLUDED.view_flow,
    tags = EXCLUDED.tags,
    updated_at = EXCLUDED.updated_at`

	// The tag predicate is a match-ANY membership test, the twin of the sqlite
	// query's json_each walk: $2 is the filter as ",a,b,"
	// (repository.TagFilter) and strpos of ",<tag>," is an exact element match,
	// so "gdpr" never matches "gdpr-lite". '' means "no filter".
	workflowFilterSQL = `($1::text = '' OR title ILIKE '%' || $1::text || '%')
		 AND ($2::text = '' OR EXISTS (
		       SELECT 1 FROM jsonb_array_elements_text(workflows.tags::jsonb) AS tag
		       WHERE strpos($2::text, ',' || tag || ',') > 0))`
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
		// …and the stored tags, unless this save carries its own (see the
		// sqlite twin: nil means "said nothing", empty means "clear").
		if w.Tags == nil {
			w.Tags = existing.Tags
		}
	} else if errors.Is(err, repository.ErrNotFound) {
		if w.CreatedAt == 0 {
			w.CreatedAt = now
		}
	} else {
		return err
	}
	w.UpdatedAt = now
	w.Tags = models.NormalizeTags(w.Tags)

	viewFlow, err := json.Marshal(w.ViewFlow)
	if err != nil {
		return fmt.Errorf("postgres: marshal view_flow: %w", err)
	}
	_, err = r.db.ExecContext(ctx, workflowUpsertSQL,
		w.ID, w.Title, string(viewFlow), marshalTags(w.Tags), w.CreatedAt, w.UpdatedAt)
	return err
}

func (r *workflowRepo) GetByID(ctx context.Context, id string) (*models.FlowRecord, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+workflowColumns+` FROM workflows WHERE id = $1`, id)
	return scanWorkflow(row)
}

func (r *workflowRepo) List(ctx context.Context, p repository.ListParams) ([]models.FlowRecord, int64, error) {
	limit := clampLimit(p.Limit)
	tags := repository.TagFilter(p.Tags)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflows WHERE `+workflowFilterSQL, p.Search, tags).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+workflowColumns+` FROM workflows WHERE `+workflowFilterSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $3 OFFSET $4`,
		p.Search, tags, limit, p.Offset)
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
		tags     string
	)
	if err := row.Scan(&rec.ID, &rec.Title, &viewFlow, &tags, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	rec.Tags = unmarshalTags(tags)
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

// marshalTags renders a tag list for the column. The column is NOT NULL and the
// tag predicate casts it to jsonb, so it always holds a JSON array.
func marshalTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// unmarshalTags decodes a tag column, tolerating the empty text an un-migrated
// row could hold. nil means "no tags" (see models.FlowRecord).
func unmarshalTags(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil || len(out) == 0 {
		return nil
	}
	return out
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
