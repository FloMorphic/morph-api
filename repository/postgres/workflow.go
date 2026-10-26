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
	workflowColumns = `id, title, view_flow, tags, preferences, plugins, created_at, updated_at`

	// A list leaves view_flow out: a flow's graph is tens of kilobytes and no
	// list renders it, so a page of records was megabytes read, decoded and
	// sent for a field every caller threw away (see models.FlowSummary). What a
	// list does show of the graph is its node count, counted here from the
	// stored JSON — the subquery is the guard: jsonb_array_length raises on a
	// value that is not an array, so a graph with no (or a non-array) `nodes`
	// member yields no row and the COALESCE answers 0. NULLIF covers the empty
	// text an un-migrated row could hold.
	// `plugins` IS selected: it is the few entries derived from that graph on
	// save (models.CollectFlowPlugins), which is what lets a list say which
	// plugins a flow needs without reading one graph.
	workflowSummaryColumns = `id, title, tags, preferences, plugins, created_at, updated_at,
		 COALESCE((SELECT jsonb_array_length(g.j -> 'nodes')
		             FROM (SELECT NULLIF(workflows.view_flow, '')::jsonb AS j) AS g
		            WHERE jsonb_typeof(g.j -> 'nodes') = 'array'), 0) AS node_count`

	workflowUpsertSQL = `
INSERT INTO workflows (id, title, view_flow, tags, preferences, plugins, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    view_flow = EXCLUDED.view_flow,
    tags = EXCLUDED.tags,
    preferences = EXCLUDED.preferences,
    plugins = EXCLUDED.plugins,
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
		// The same for the preferences: a graph-only save keeps the stored entry
		// form, a non-nil value with no entry clears it.
		if w.Preferences == nil {
			w.Preferences = existing.Preferences
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
	w.Preferences = w.Preferences.Normalize()
	// Derived from the graph being saved, never taken from the caller — the
	// sqlite twin says why.
	w.Plugins = models.CollectFlowPlugins(w.ViewFlow)

	viewFlow, err := json.Marshal(w.ViewFlow)
	if err != nil {
		return fmt.Errorf("postgres: marshal view_flow: %w", err)
	}
	preferences, err := marshalPreferences(w.Preferences)
	if err != nil {
		return fmt.Errorf("postgres: marshal preferences: %w", err)
	}
	plugins, err := marshalPlugins(w.Plugins)
	if err != nil {
		return fmt.Errorf("postgres: marshal plugins: %w", err)
	}
	_, err = r.db.ExecContext(ctx, workflowUpsertSQL,
		w.ID, w.Title, string(viewFlow), marshalTags(w.Tags), preferences, plugins, w.CreatedAt, w.UpdatedAt)
	return err
}

func (r *workflowRepo) GetByID(ctx context.Context, id string) (*models.FlowRecord, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+workflowColumns+` FROM workflows WHERE id = $1`, id)
	return scanWorkflow(row)
}

// List answers with summaries, not records: the graph is read by GetByID, for
// the one flow that is actually opened (see repository.WorkflowRepository).
func (r *workflowRepo) List(ctx context.Context, p repository.ListParams) ([]models.FlowSummary, int64, error) {
	limit := clampLimit(p.Limit)
	tags := repository.TagFilter(p.Tags)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workflows WHERE `+workflowFilterSQL, p.Search, tags).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+workflowSummaryColumns+` FROM workflows WHERE `+workflowFilterSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $3 OFFSET $4`,
		p.Search, tags, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]models.FlowSummary, 0, limit)
	for rows.Next() {
		sum, err := scanWorkflowSummary(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *sum)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListRecords is List's twin for the caller that wants the graphs (see
// repository.WorkflowRepository): the same page and count, selecting the record
// columns — view_flow among them — instead of the summary ones.
func (r *workflowRepo) ListRecords(ctx context.Context, p repository.ListParams) ([]models.FlowRecord, int64, error) {
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
		rec         models.FlowRecord
		viewFlow    string
		tags        string
		preferences string
		plugins     string
	)
	if err := row.Scan(&rec.ID, &rec.Title, &viewFlow, &tags, &preferences, &plugins, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	rec.Tags = unmarshalTags(tags)
	prefs, err := unmarshalPreferences(preferences)
	if err != nil {
		return nil, fmt.Errorf("postgres: unmarshal preferences for %s: %w", rec.ID, err)
	}
	rec.Preferences = prefs
	rec.Plugins = unmarshalPlugins(plugins)
	if viewFlow != "" {
		if err := json.Unmarshal([]byte(viewFlow), &rec.ViewFlow); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal view_flow for %s: %w", rec.ID, err)
		}
	}
	return &rec, nil
}

// scanWorkflowSummary maps one list row — the record's columns minus the graph,
// plus the node count the query derived from it.
func scanWorkflowSummary(row scanner) (*models.FlowSummary, error) {
	var (
		sum         models.FlowSummary
		tags        string
		preferences string
		plugins     string
	)
	if err := row.Scan(&sum.ID, &sum.Title, &tags, &preferences, &plugins, &sum.CreatedAt, &sum.UpdatedAt, &sum.NodeCount); err != nil {
		return nil, notFound(err)
	}
	sum.Tags = unmarshalTags(tags)
	prefs, err := unmarshalPreferences(preferences)
	if err != nil {
		return nil, fmt.Errorf("postgres: unmarshal preferences for %s: %w", sum.ID, err)
	}
	sum.Preferences = prefs
	sum.Plugins = unmarshalPlugins(plugins)
	return &sum, nil
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

// marshalPreferences / unmarshalPreferences are the postgres twins of the
// sqlite pair: "no preferences" is the NOT NULL column's `{}`, and a read gives
// back nil for it so a later save that says nothing about preferences keeps
// what is stored (see Upsert).
func marshalPreferences(p *models.FlowPreferences) (string, error) {
	if p.IsEmpty() {
		return "{}", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalPreferences(s string) (*models.FlowPreferences, error) {
	if s == "" || s == "{}" || s == "null" {
		return nil, nil
	}
	var p models.FlowPreferences
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	if p.Normalize().IsEmpty() {
		return nil, nil
	}
	return &p, nil
}

// marshalPlugins / unmarshalPlugins are the postgres twins of the sqlite pair:
// "no plugins" is the NOT NULL column's `[]`, and a read tolerates both that
// and the empty text a row written before the column existed holds. An
// undecodable list reads as none rather than failing the read — it is derived
// data the next save rebuilds from the graph.
func marshalPlugins(p []models.FlowPlugin) (string, error) {
	if len(p) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalPlugins(s string) []models.FlowPlugin {
	if s == "" || s == "[]" || s == "null" {
		return nil
	}
	var out []models.FlowPlugin
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

// backfillWorkflowPlugins derives the plugin list of the flows saved before the
// column existed, and reports how many it filled. The sqlite twin explains the
// sentinel (the empty text an existing row carries) and why the rows are
// collected before they are written.
func backfillWorkflowPlugins(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, view_flow FROM workflows WHERE plugins = ''`)
	if err != nil {
		return 0, err
	}
	type pending struct{ id, plugins string }
	var todo []pending
	for rows.Next() {
		var id, viewFlow string
		if err := rows.Scan(&id, &viewFlow); err != nil {
			rows.Close()
			return 0, err
		}
		var rec models.FlowRecord
		if viewFlow != "" {
			if err := json.Unmarshal([]byte(viewFlow), &rec.ViewFlow); err != nil {
				todo = append(todo, pending{id: id, plugins: "[]"})
				continue
			}
		}
		plugins, err := marshalPlugins(models.CollectFlowPlugins(rec.ViewFlow))
		if err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, pending{id: id, plugins: plugins})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, p := range todo {
		// updated_at untouched: deriving a field is not a change to the flow.
		if _, err := db.ExecContext(ctx,
			`UPDATE workflows SET plugins = $1 WHERE id = $2`, p.plugins, p.id); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}
