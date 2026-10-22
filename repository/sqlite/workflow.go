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

	viewFlow, err := json.Marshal(w.ViewFlow)
	if err != nil {
		return fmt.Errorf("sqlite: marshal view_flow: %w", err)
	}
	preferences, err := marshalPreferences(w.Preferences)
	if err != nil {
		return fmt.Errorf("sqlite: marshal preferences: %w", err)
	}
	return r.q.UpsertWorkflow(ctx, sqlcgen.UpsertWorkflowParams{
		ID:          w.ID,
		Title:       w.Title,
		ViewFlow:    string(viewFlow),
		Tags:        marshalTags(w.Tags),
		Preferences: preferences,
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

func (r *workflowRepo) List(ctx context.Context, p repository.ListParams) ([]models.FlowRecord, int64, error) {
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
