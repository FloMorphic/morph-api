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

type workflowRepo struct {
	q *sqlcgen.Queries
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
		// …and the stored tags, unless this save carries its own. A nil Tags is
		// a client that said nothing about them (an MCP patch, the editor
		// before tags existed), and must not strip the labels a package
		// stamped on the flow; an empty non-nil slice clears them.
		if w.Tags == nil {
			w.Tags = existing.Tags
		}
		// The same contract for the preferences: nil says nothing (keep the
		// stored entry form — a graph-only save from any client must not drop
		// it), a non-nil value with no entry clears them.
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
	// The flow's plugin list is DERIVED from the graph being saved, never taken
	// from the caller: deriving it here — the one place every write path goes
	// through, whatever drew the graph (the canvas, an import, an MCP patch,
	// a relabel that resends the record) — is what keeps the stored list and
	// the stored graph from ever telling two different stories.
	w.Plugins = models.CollectFlowPlugins(w.ViewFlow)

	viewFlow, err := json.Marshal(w.ViewFlow)
	if err != nil {
		return fmt.Errorf("sqlite: marshal view_flow: %w", err)
	}
	preferences, err := marshalPreferences(w.Preferences)
	if err != nil {
		return fmt.Errorf("sqlite: marshal preferences: %w", err)
	}
	plugins, err := marshalPlugins(w.Plugins)
	if err != nil {
		return fmt.Errorf("sqlite: marshal plugins: %w", err)
	}
	return r.q.UpsertWorkflow(ctx, sqlcgen.UpsertWorkflowParams{
		ID:          w.ID,
		Title:       w.Title,
		ViewFlow:    string(viewFlow),
		Tags:        marshalTags(w.Tags),
		Preferences: preferences,
		Plugins:     plugins,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	})
}

func (r *workflowRepo) GetByID(ctx context.Context, id string) (*models.FlowRecord, error) {
	row, err := r.q.GetWorkflow(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return workflowFromRow(row)
}

// List answers with summaries, not records: the query leaves view_flow out of
// the SELECT (see queries/workflows.sql), so a page costs neither the graph
// text nor a JSON decode per row.
func (r *workflowRepo) List(ctx context.Context, p repository.ListParams) ([]models.FlowSummary, int64, error) {
	limit := clampLimit(p.Limit)
	tags := repository.TagFilter(p.Tags)
	total, err := r.q.CountWorkflows(ctx, sqlcgen.CountWorkflowsParams{Search: p.Search, Tags: tags})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListWorkflows(ctx, sqlcgen.ListWorkflowsParams{
		Search: p.Search,
		Tags:   tags,
		Offset: int64(p.Offset),
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.FlowSummary, 0, len(rows))
	for _, row := range rows {
		prefs, err := unmarshalPreferences(row.Preferences)
		if err != nil {
			return nil, 0, fmt.Errorf("sqlite: unmarshal preferences for %s: %w", row.ID, err)
		}
		items = append(items, models.FlowSummary{
			ID:          row.ID,
			Title:       row.Title,
			Tags:        unmarshalTags(row.Tags),
			Preferences: prefs,
			Plugins:     unmarshalPlugins(row.Plugins),
			NodeCount:   int(row.NodeCount),
			CreatedAt:   row.CreatedAt,
			UpdatedAt:   row.UpdatedAt,
		})
	}
	return items, total, nil
}

// ListRecords is List's twin for the caller that wants the graphs (see
// repository.WorkflowRepository): the same page and the same count, read
// through the query that does select view_flow.
func (r *workflowRepo) ListRecords(ctx context.Context, p repository.ListParams) ([]models.FlowRecord, int64, error) {
	limit := clampLimit(p.Limit)
	tags := repository.TagFilter(p.Tags)
	total, err := r.q.CountWorkflows(ctx, sqlcgen.CountWorkflowsParams{Search: p.Search, Tags: tags})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListWorkflowRecords(ctx, sqlcgen.ListWorkflowRecordsParams{
		Search: p.Search,
		Tags:   tags,
		Offset: int64(p.Offset),
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.FlowRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := workflowFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *workflowRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func workflowFromRow(row sqlcgen.Workflow) (*models.FlowRecord, error) {
	prefs, err := unmarshalPreferences(row.Preferences)
	if err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal preferences for %s: %w", row.ID, err)
	}
	rec := &models.FlowRecord{
		ID:          row.ID,
		Title:       row.Title,
		Tags:        unmarshalTags(row.Tags),
		Preferences: prefs,
		Plugins:     unmarshalPlugins(row.Plugins),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.ViewFlow != "" {
		if err := json.Unmarshal([]byte(row.ViewFlow), &rec.ViewFlow); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal view_flow for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}

// marshalTags renders a tag list for the column. The column is NOT NULL and the
// tag predicate feeds it to json_each, so it always holds a JSON array — never
// "" and never null.
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

// unmarshalTags decodes a tag column, tolerating the empty text an
// un-migrated row could hold. It returns nil for "no tags", which is what the
// model's omitempty wants, and leaves the stored value in place on an upsert
// (see Upsert) — a read never has to distinguish nil from empty.
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

// marshalPreferences renders a flow's preferences for the column. The column is
// NOT NULL, so "no preferences" is stored as `{}` — never "" and never null.
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

// unmarshalPreferences decodes the column, tolerating the empty text an
// un-migrated row holds. It returns nil for "none", which is what the model's
// "a save that says nothing keeps what is stored" contract reads as — a read
// never has to tell nil from empty (see Upsert).
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

// marshalPlugins renders a flow's derived plugin list for the column. The
// column is NOT NULL, so "no plugins" is stored as `[]` — never "" and never
// null.
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

// unmarshalPlugins decodes the column, tolerating the empty text a row written
// before the column existed holds. A row that has not been saved since then
// reads as no plugins — and gets its list on its next save, which is also the
// moment the graph it describes could last have changed. Undecodable text is
// read the same way rather than failing the read: the list is derived data a
// save rebuilds, and it is not worth refusing a flow over.
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

// backfillWorkflowPlugins derives the plugin list of the flows that were saved
// before the column existed, and reports how many it filled.
//
// The list is otherwise written by a save, which would mean an install's
// existing flows all claim to need no plugin until someone opens and re-saves
// each one — a wrong answer that looks like a real one. The sentinel is the
// empty text the ALTER leaves on an existing row: a flow that genuinely needs
// no plugin is stored as `[]`, so this pass runs once and then finds nothing.
//
// Every row is read and updated in two phases rather than in one cursor walk:
// the sqlite pool is a single connection (see Open), so an UPDATE issued while
// a SELECT is still streaming would wait for a connection that the SELECT holds.
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
				// A graph that cannot be read is nothing to derive from, and
				// marking the row done is what keeps this a one-time pass.
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
		// updated_at is deliberately left alone: deriving a field the save
		// should have written is not a change to the flow, and it must not
		// reorder a list that is sorted by it.
		if _, err := db.ExecContext(ctx,
			`UPDATE workflows SET plugins = ? WHERE id = ?`, p.plugins, p.id); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}
