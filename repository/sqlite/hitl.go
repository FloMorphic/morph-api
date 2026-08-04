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

type humanTaskRepo struct {
	q *sqlcgen.Queries
}

func (r *humanTaskRepo) Upsert(ctx context.Context, t *models.HumanTask) error {
	now := nowMillis()
	if t.ID == "" {
		t.ID = repository.NewID(repository.HumanTaskIDPrefix)
		if t.CreatedAt == 0 {
			t.CreatedAt = now
		}
	} else if existing, err := r.GetByID(ctx, t.ID); err == nil {
		t.CreatedAt = existing.CreatedAt
	} else if errors.Is(err, repository.ErrNotFound) {
		if t.CreatedAt == 0 {
			t.CreatedAt = now
		}
	} else {
		return err
	}
	t.UpdatedAt = now

	if t.Status == "" {
		t.Status = models.HumanTaskOpen
	}
	if t.Questions == nil {
		t.Questions = []models.HumanTaskQuestion{}
	}
	if t.Messages == nil {
		t.Messages = []models.HumanTaskMessage{}
	}
	// A task recorded by an older node (or a malformed op payload) still gets the
	// behaviour the node used to have: park, answered in the app.
	if t.Mode == "" {
		t.Mode = models.HumanTaskPark
	}
	if t.Channel == "" {
		t.Channel = models.HumanTaskDirect
	}

	questions, err := json.Marshal(t.Questions)
	if err != nil {
		return fmt.Errorf("sqlite: marshal human task questions: %w", err)
	}
	messages, err := json.Marshal(t.Messages)
	if err != nil {
		return fmt.Errorf("sqlite: marshal human task messages: %w", err)
	}
	data := "{}"
	if t.Data != nil {
		b, err := json.Marshal(t.Data)
		if err != nil {
			return fmt.Errorf("sqlite: marshal human task data: %w", err)
		}
		data = string(b)
	}
	nexts, err := json.Marshal(t.Nexts)
	if err != nil {
		return fmt.Errorf("sqlite: marshal human task nexts: %w", err)
	}

	return r.q.UpsertHumanTask(ctx, sqlcgen.UpsertHumanTaskParams{
		ID:        t.ID,
		Title:     t.Title,
		Status:    string(t.Status),
		Pid:       t.PID,
		FlowID:    t.FlowID,
		NodeID:    t.NodeID,
		ContextID: t.ContextID,
		Mode:      string(t.Mode),
		Channel:   string(t.Channel),
		Prompt:    t.Prompt,
		Questions: string(questions),
		Messages:  string(messages),
		Data:      data,
		Nexts:     string(nexts),
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
		ClosedAt:  t.ClosedAt,
	})
}

func (r *humanTaskRepo) GetByID(ctx context.Context, id string) (*models.HumanTask, error) {
	row, err := r.q.GetHumanTask(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return humanTaskFromRow(row)
}

func (r *humanTaskRepo) List(ctx context.Context, p repository.ListParams) ([]models.HumanTask, int64, error) {
	limit := clampLimit(p.Limit)
	total, err := r.q.CountHumanTasks(ctx, sqlcgen.CountHumanTasksParams{
		Search: p.Search,
		Status: p.Status,
	})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListHumanTasks(ctx, sqlcgen.ListHumanTasksParams{
		Search: p.Search,
		Status: p.Status,
		Offset: int64(p.Offset),
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.HumanTask, 0, len(rows))
	for _, row := range rows {
		rec, err := humanTaskFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *humanTaskRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteHumanTask(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *humanTaskRepo) Answer(ctx context.Context, id, questionID, answer string) (*models.HumanTask, error) {
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Status == models.HumanTaskClosed {
		return nil, fmt.Errorf("human task %s is closed", id)
	}
	found := false
	for i := range task.Questions {
		if task.Questions[i].ID == questionID {
			task.Questions[i].Answer = answer
			task.Questions[i].AnsweredAt = nowMillis()
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("question %s not found on task %s", questionID, id)
	}
	// Flip to answered once every question carries an answer.
	if task.AllAnswered() {
		task.Status = models.HumanTaskAnswered
	} else {
		task.Status = models.HumanTaskOpen
	}
	if err := r.Upsert(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (r *humanTaskRepo) AppendMessage(ctx context.Context, id string, msg models.HumanTaskMessage) (*models.HumanTask, error) {
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if msg.At == 0 {
		msg.At = nowMillis()
	}
	if msg.ID == "" {
		msg.ID = repository.NewID("msg")
	}
	task.Messages = append(task.Messages, msg)
	if err := r.Upsert(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (r *humanTaskRepo) Close(ctx context.Context, id string) (*models.HumanTask, error) {
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	task.Status = models.HumanTaskClosed
	task.ClosedAt = nowMillis()
	if err := r.Upsert(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

func humanTaskFromRow(row sqlcgen.HumanTask) (*models.HumanTask, error) {
	rec := &models.HumanTask{
		ID:        row.ID,
		Title:     row.Title,
		Status:    models.HumanTaskStatus(row.Status),
		PID:       row.Pid,
		FlowID:    row.FlowID,
		NodeID:    row.NodeID,
		ContextID: row.ContextID,
		Mode:      models.HumanTaskMode(row.Mode),
		Channel:   models.HumanTaskChannel(row.Channel),
		Prompt:    row.Prompt,
		Questions: []models.HumanTaskQuestion{},
		Messages:  []models.HumanTaskMessage{},
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
		ClosedAt:  row.ClosedAt,
	}
	if row.Questions != "" {
		if err := json.Unmarshal([]byte(row.Questions), &rec.Questions); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal human task questions for %s: %w", row.ID, err)
		}
	}
	if row.Messages != "" {
		if err := json.Unmarshal([]byte(row.Messages), &rec.Messages); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal human task messages for %s: %w", row.ID, err)
		}
	}
	if row.Data != "" && row.Data != "{}" {
		if err := json.Unmarshal([]byte(row.Data), &rec.Data); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal human task data for %s: %w", row.ID, err)
		}
	}
	if row.Nexts != "" && row.Nexts != "[]" && row.Nexts != "null" {
		if err := json.Unmarshal([]byte(row.Nexts), &rec.Nexts); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal human task nexts for %s: %w", row.ID, err)
		}
	}
	return rec, nil
}
