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

type processRepo struct {
	q *sqlcgen.Queries
}

func (r *processRepo) Create(ctx context.Context, p *models.Process) error {
	now := nowMillis()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if p.Status == "" {
		p.Status = models.ProcessRunning
	}

	request, err := marshalObject(p.Request)
	if err != nil {
		return fmt.Errorf("sqlite: marshal process request: %w", err)
	}
	meta, err := marshalObject(p.Meta)
	if err != nil {
		return fmt.Errorf("sqlite: marshal process meta: %w", err)
	}
	snapshot, err := marshalObject(p.Snapshot)
	if err != nil {
		return fmt.Errorf("sqlite: marshal process snapshot: %w", err)
	}

	res, err := r.q.InsertProcess(ctx, sqlcgen.InsertProcessParams{
		Pid:         p.PID,
		InstanceID:  p.InstanceID,
		FlowID:      p.FlowID,
		ContextID:   p.ContextID,
		StartNodeID: p.StartNodeID,
		Status:      string(p.Status),
		ResourceUrl: p.ResourceURL,
		Request:     request,
		Meta:        meta,
		Snapshot:    snapshot,
		Error:       p.Error,
		ScheduledAt: p.ScheduledAt,
		StartedAt:   p.StartedAt,
		FinishedAt:  p.FinishedAt,
		DurationMs:  p.DurationMs,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	})
	if err != nil {
		return err
	}
	p.IndexID, err = res.LastInsertId()
	return err
}

func (r *processRepo) Update(ctx context.Context, p *models.Process) error {
	if p.IndexID == 0 {
		return fmt.Errorf("sqlite: update process: indexId is required")
	}
	p.UpdatedAt = nowMillis()
	if p.Status == "" {
		p.Status = models.ProcessRunning
	}

	request, err := marshalObject(p.Request)
	if err != nil {
		return fmt.Errorf("sqlite: marshal process request: %w", err)
	}
	meta, err := marshalObject(p.Meta)
	if err != nil {
		return fmt.Errorf("sqlite: marshal process meta: %w", err)
	}
	snapshot, err := marshalObject(p.Snapshot)
	if err != nil {
		return fmt.Errorf("sqlite: marshal process snapshot: %w", err)
	}

	n, err := r.q.UpdateProcess(ctx, sqlcgen.UpdateProcessParams{
		IndexID:     p.IndexID,
		Pid:         p.PID,
		InstanceID:  p.InstanceID,
		FlowID:      p.FlowID,
		ContextID:   p.ContextID,
		StartNodeID: p.StartNodeID,
		Status:      string(p.Status),
		ResourceUrl: p.ResourceURL,
		Request:     request,
		Meta:        meta,
		Snapshot:    snapshot,
		Error:       p.Error,
		ScheduledAt: p.ScheduledAt,
		StartedAt:   p.StartedAt,
		FinishedAt:  p.FinishedAt,
		DurationMs:  p.DurationMs,
		UpdatedAt:   p.UpdatedAt,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *processRepo) GetByIndex(ctx context.Context, indexID int64) (*models.Process, error) {
	row, err := r.q.GetProcess(ctx, indexID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return processFromRow(row)
}

func (r *processRepo) GetRunningByPID(ctx context.Context, pid string) (*models.Process, error) {
	row, err := r.q.GetRunningProcessByPID(ctx, pid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return processFromRow(row)
}

func (r *processRepo) NextScheduled(ctx context.Context) (*models.Process, error) {
	row, err := r.q.GetNextScheduledProcess(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return processFromRow(row)
}

func (r *processRepo) ListDueScheduled(ctx context.Context, now int64) ([]models.Process, error) {
	rows, err := r.q.ListDueScheduledProcesses(ctx, now)
	if err != nil {
		return nil, err
	}
	items := make([]models.Process, 0, len(rows))
	for _, row := range rows {
		rec, err := processFromRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, *rec)
	}
	return items, nil
}

func (r *processRepo) List(ctx context.Context, p repository.ListParams) ([]models.Process, int64, error) {
	limit := clampLimit(p.Limit)
	total, err := r.q.CountProcesses(ctx, sqlcgen.CountProcessesParams{
		Search:     p.Search,
		Status:     p.Status,
		Pid:        p.PID,
		FlowID:     p.FlowID,
		InstanceID: p.InstanceID,
	})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListProcesses(ctx, sqlcgen.ListProcessesParams{
		Search:     p.Search,
		Status:     p.Status,
		Pid:        p.PID,
		FlowID:     p.FlowID,
		InstanceID: p.InstanceID,
		Offset:     int64(p.Offset),
		Limit:      int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.Process, 0, len(rows))
	for _, row := range rows {
		rec, err := processFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *processRepo) Delete(ctx context.Context, indexID int64) error {
	n, err := r.q.DeleteProcess(ctx, indexID)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func processFromRow(row sqlcgen.Process) (*models.Process, error) {
	rec := &models.Process{
		IndexID:     row.IndexID,
		PID:         row.Pid,
		InstanceID:  row.InstanceID,
		FlowID:      row.FlowID,
		ContextID:   row.ContextID,
		StartNodeID: row.StartNodeID,
		Status:      models.ProcessStatus(row.Status),
		ResourceURL: row.ResourceUrl,
		Error:       row.Error,
		ScheduledAt: row.ScheduledAt,
		StartedAt:   row.StartedAt,
		FinishedAt:  row.FinishedAt,
		DurationMs:  row.DurationMs,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if err := unmarshalObject(row.Request, &rec.Request); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal process request for %d: %w", row.IndexID, err)
	}
	if err := unmarshalObject(row.Meta, &rec.Meta); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal process meta for %d: %w", row.IndexID, err)
	}
	if err := unmarshalObject(row.Snapshot, &rec.Snapshot); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal process snapshot for %d: %w", row.IndexID, err)
	}
	return rec, nil
}

// marshalObject renders a free-form object column, defaulting a nil map to "{}".
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
