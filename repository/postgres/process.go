package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
)

const (
	processColumns = `index_id, pid, instance_id, flow_id, context_id, start_node_id, status,
	resource_url, request, meta, snapshot, errors, error, scheduled_at, started_at,
	finished_at, duration_ms, created_at, updated_at`

	// RETURNING is how the assigned index comes back in one round trip. The
	// launch path needs it immediately: it echoes the index into the
	// ProcessRequest meta before the request is dispatched.
	processInsertSQL = `
INSERT INTO processes (
    pid, instance_id, flow_id, context_id, start_node_id, status, resource_url,
    request, meta, snapshot, errors, error, scheduled_at, started_at, finished_at, duration_ms,
    created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
RETURNING index_id`

	processUpdateSQL = `
UPDATE processes SET
    pid = $1,
    instance_id = $2,
    flow_id = $3,
    context_id = $4,
    start_node_id = $5,
    status = $6,
    resource_url = $7,
    request = $8,
    meta = $9,
    snapshot = $10,
    errors = $11,
    error = $12,
    scheduled_at = $13,
    started_at = $14,
    finished_at = $15,
    duration_ms = $16,
    updated_at = $17
WHERE index_id = $18`

	// $1 search (pid or flow), $2 status, $3 pid, $4 flow, $5 instance.
	processFilterSQL = `($1::text = '' OR pid ILIKE '%' || $1::text || '%' OR flow_id ILIKE '%' || $1::text || '%')
	  AND ($2::text = '' OR status = $2::text)
	  AND ($3::text = '' OR pid = $3::text)
	  AND ($4::text = '' OR flow_id = $4::text)
	  AND ($5::text = '' OR instance_id = $5::text)`
)

type processRepo struct {
	db *sql.DB
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

	cols, err := processPayload(p)
	if err != nil {
		return err
	}
	return r.db.QueryRowContext(ctx, processInsertSQL,
		p.PID, p.InstanceID, p.FlowID, p.ContextID, p.StartNodeID, string(p.Status), p.ResourceURL,
		cols.request, cols.meta, cols.snapshot, cols.errors, p.Error,
		p.ScheduledAt, p.StartedAt, p.FinishedAt, p.DurationMs, p.CreatedAt, p.UpdatedAt,
	).Scan(&p.IndexID)
}

func (r *processRepo) Update(ctx context.Context, p *models.Process) error {
	if p.IndexID == 0 {
		return fmt.Errorf("postgres: update process: indexId is required")
	}
	p.UpdatedAt = nowMillis()
	if p.Status == "" {
		p.Status = models.ProcessRunning
	}

	cols, err := processPayload(p)
	if err != nil {
		return err
	}
	return execExpectRow(ctx, r.db, processUpdateSQL,
		p.PID, p.InstanceID, p.FlowID, p.ContextID, p.StartNodeID, string(p.Status), p.ResourceURL,
		cols.request, cols.meta, cols.snapshot, cols.errors, p.Error,
		p.ScheduledAt, p.StartedAt, p.FinishedAt, p.DurationMs, p.UpdatedAt, p.IndexID)
}

func (r *processRepo) GetByIndex(ctx context.Context, indexID int64) (*models.Process, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+processColumns+` FROM processes WHERE index_id = $1`, indexID)
	return scanProcess(row)
}

// GetRunningByPID resolves the single `running` row for an engine process uuid —
// how a proc.finish event (which carries only the pid) finds the row to close.
func (r *processRepo) GetRunningByPID(ctx context.Context, pid string) (*models.Process, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+processColumns+` FROM processes
		 WHERE pid = $1 AND status = 'running'
		 ORDER BY index_id DESC LIMIT 1`, pid)
	return scanProcess(row)
}

// NextScheduled returns the soonest `scheduled` row — the head of the queue the
// scheduler arms its timer on.
func (r *processRepo) NextScheduled(ctx context.Context) (*models.Process, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+processColumns+` FROM processes
		 WHERE status = 'scheduled'
		 ORDER BY scheduled_at ASC, index_id ASC LIMIT 1`)
	return scanProcess(row)
}

// ListDueScheduled returns every `scheduled` row whose time has come, soonest
// first — the batch the scheduler dispatches when its timer fires.
func (r *processRepo) ListDueScheduled(ctx context.Context, now int64) ([]models.Process, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+processColumns+` FROM processes
		 WHERE status = 'scheduled' AND scheduled_at <= $1
		 ORDER BY scheduled_at ASC, index_id ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectProcesses(rows, 16)
}

func (r *processRepo) List(ctx context.Context, p repository.ListParams) ([]models.Process, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM processes WHERE `+processFilterSQL,
		p.Search, p.Status, p.PID, p.FlowID, p.InstanceID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+processColumns+` FROM processes WHERE `+processFilterSQL+`
		 ORDER BY updated_at DESC, index_id DESC LIMIT $6 OFFSET $7`,
		p.Search, p.Status, p.PID, p.FlowID, p.InstanceID, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := collectProcesses(rows, limit)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *processRepo) Delete(ctx context.Context, indexID int64) error {
	return execExpectRow(ctx, r.db, `DELETE FROM processes WHERE index_id = $1`, indexID)
}

// processJSON holds a run's four free-form object columns, rendered once and
// reused by the insert and the update.
type processJSON struct {
	request, meta, snapshot, errors string
}

func processPayload(p *models.Process) (processJSON, error) {
	var (
		out processJSON
		err error
	)
	if out.request, err = marshalObject(p.Request); err != nil {
		return out, fmt.Errorf("postgres: marshal process request: %w", err)
	}
	if out.meta, err = marshalObject(p.Meta); err != nil {
		return out, fmt.Errorf("postgres: marshal process meta: %w", err)
	}
	if out.snapshot, err = marshalObject(p.Snapshot); err != nil {
		return out, fmt.Errorf("postgres: marshal process snapshot: %w", err)
	}
	if out.errors, err = marshalObject(p.Errors); err != nil {
		return out, fmt.Errorf("postgres: marshal process errors: %w", err)
	}
	return out, nil
}

func collectProcesses(rows *sql.Rows, capacity int) ([]models.Process, error) {
	items := make([]models.Process, 0, capacity)
	for rows.Next() {
		rec, err := scanProcess(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func scanProcess(row scanner) (*models.Process, error) {
	var (
		rec                                models.Process
		status                             string
		request, meta, snapshot, runErrors string
	)
	if err := row.Scan(&rec.IndexID, &rec.PID, &rec.InstanceID, &rec.FlowID, &rec.ContextID,
		&rec.StartNodeID, &status, &rec.ResourceURL, &request, &meta, &snapshot, &runErrors,
		&rec.Error, &rec.ScheduledAt, &rec.StartedAt, &rec.FinishedAt, &rec.DurationMs,
		&rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	rec.Status = models.ProcessStatus(status)
	if err := unmarshalObject(request, &rec.Request); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal process request for %d: %w", rec.IndexID, err)
	}
	if err := unmarshalObject(meta, &rec.Meta); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal process meta for %d: %w", rec.IndexID, err)
	}
	if err := unmarshalObject(snapshot, &rec.Snapshot); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal process snapshot for %d: %w", rec.IndexID, err)
	}
	if err := unmarshalObject(runErrors, &rec.Errors); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal process errors for %d: %w", rec.IndexID, err)
	}
	return &rec, nil
}
