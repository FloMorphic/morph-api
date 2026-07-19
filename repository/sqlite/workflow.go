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
		return fmt.Errorf("sqlite: marshal view_flow: %w", err)
	}
	return r.q.UpsertWorkflow(ctx, sqlcgen.UpsertWorkflowParams{
		ID:        w.ID,
		Title:     w.Title,
		ViewFlow:  string(viewFlow),
		CreatedAt: w.CreatedAt,
		UpdatedAt: w.UpdatedAt,
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
	total, err := r.q.CountWorkflows(ctx, p.Search)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListWorkflows(ctx, sqlcgen.ListWorkflowsParams{
		Search: p.Search,
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
	rec := &models.FlowRecord{
		ID:        row.ID,
		Title:     row.Title,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.ViewFlow != "" {
		if err := json.Unmarshal([]byte(row.ViewFlow), &rec.ViewFlow); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal view_flow for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}
