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
	humanTaskColumns = `id, title, status, pid, instance_id, flow_id, node_id, context_id,
	mode, channel, telegram, prompt, settings_id, node_key,
	questions, messages, data, nexts, created_at, updated_at, closed_at`

	humanTaskUpsertSQL = `
INSERT INTO human_tasks (
    id, title, status, pid, instance_id, flow_id, node_id, context_id,
    mode, channel, telegram, prompt, settings_id, node_key,
    questions, messages, data, nexts, created_at, updated_at, closed_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8,
    $9, $10, $11, $12, $13, $14,
    $15, $16, $17, $18, $19, $20, $21
)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    status = EXCLUDED.status,
    pid = EXCLUDED.pid,
    instance_id = EXCLUDED.instance_id,
    flow_id = EXCLUDED.flow_id,
    node_id = EXCLUDED.node_id,
    context_id = EXCLUDED.context_id,
    mode = EXCLUDED.mode,
    channel = EXCLUDED.channel,
    telegram = EXCLUDED.telegram,
    prompt = EXCLUDED.prompt,
    settings_id = EXCLUDED.settings_id,
    node_key = EXCLUDED.node_key,
    questions = EXCLUDED.questions,
    messages = EXCLUDED.messages,
    data = EXCLUDED.data,
    nexts = EXCLUDED.nexts,
    updated_at = EXCLUDED.updated_at,
    closed_at = EXCLUDED.closed_at`

	// $1 search, $2 status, $3 flow — the drawer lists one flow's open tasks.
	humanTaskFilterSQL = `($1::text = '' OR title ILIKE '%' || $1::text || '%')
	  AND ($2::text = '' OR status = $2::text)
	  AND ($3::text = '' OR flow_id = $3::text)`
)

type humanTaskRepo struct {
	db *sql.DB
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
		return fmt.Errorf("postgres: marshal human task questions: %w", err)
	}
	messages, err := json.Marshal(t.Messages)
	if err != nil {
		return fmt.Errorf("postgres: marshal human task messages: %w", err)
	}
	data := "{}"
	if t.Data != nil {
		b, err := json.Marshal(t.Data)
		if err != nil {
			return fmt.Errorf("postgres: marshal human task data: %w", err)
		}
		data = string(b)
	}
	nexts, err := json.Marshal(t.Nexts)
	if err != nil {
		return fmt.Errorf("postgres: marshal human task nexts: %w", err)
	}
	// The telegram binding is a nullable column in Go terms — an absent one is
	// stored as `{}` so the NOT NULL column always holds valid JSON.
	telegram := "{}"
	if t.Telegram != nil {
		b, err := json.Marshal(t.Telegram)
		if err != nil {
			return fmt.Errorf("postgres: marshal human task telegram: %w", err)
		}
		telegram = string(b)
	}

	_, err = r.db.ExecContext(ctx, humanTaskUpsertSQL,
		t.ID, t.Title, string(t.Status), t.PID, t.InstanceID, t.FlowID, t.NodeID, t.ContextID,
		string(t.Mode), string(t.Channel), telegram, t.Prompt, t.SettingsID, t.Key,
		string(questions), string(messages), data, string(nexts), t.CreatedAt, t.UpdatedAt, t.ClosedAt)
	return err
}

func (r *humanTaskRepo) GetByID(ctx context.Context, id string) (*models.HumanTask, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+humanTaskColumns+` FROM human_tasks WHERE id = $1`, id)
	return scanHumanTask(row)
}

func (r *humanTaskRepo) List(ctx context.Context, p repository.ListParams) ([]models.HumanTask, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM human_tasks WHERE `+humanTaskFilterSQL,
		p.Search, p.Status, p.FlowID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+humanTaskColumns+` FROM human_tasks WHERE `+humanTaskFilterSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $4 OFFSET $5`,
		p.Search, p.Status, p.FlowID, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]models.HumanTask, 0, limit)
	for rows.Next() {
		rec, err := scanHumanTask(rows)
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

func (r *humanTaskRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM human_tasks WHERE id = $1`, id)
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

func scanHumanTask(row scanner) (*models.HumanTask, error) {
	rec := &models.HumanTask{
		Questions: []models.HumanTaskQuestion{},
		Messages:  []models.HumanTaskMessage{},
	}
	var (
		status, mode, channel                      string
		telegram, questions, messages, data, nexts string
	)
	if err := row.Scan(&rec.ID, &rec.Title, &status, &rec.PID, &rec.InstanceID, &rec.FlowID,
		&rec.NodeID, &rec.ContextID, &mode, &channel, &telegram, &rec.Prompt, &rec.SettingsID,
		&rec.Key, &questions, &messages, &data, &nexts,
		&rec.CreatedAt, &rec.UpdatedAt, &rec.ClosedAt); err != nil {
		return nil, notFound(err)
	}
	rec.Status = models.HumanTaskStatus(status)
	rec.Mode = models.HumanTaskMode(mode)
	rec.Channel = models.HumanTaskChannel(channel)

	if questions != "" {
		if err := json.Unmarshal([]byte(questions), &rec.Questions); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal human task questions for %s: %w", rec.ID, err)
		}
	}
	if messages != "" {
		if err := json.Unmarshal([]byte(messages), &rec.Messages); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal human task messages for %s: %w", rec.ID, err)
		}
	}
	if data != "" && data != "{}" {
		if err := json.Unmarshal([]byte(data), &rec.Data); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal human task data for %s: %w", rec.ID, err)
		}
	}
	if nexts != "" && nexts != "[]" && nexts != "null" {
		if err := json.Unmarshal([]byte(nexts), &rec.Nexts); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal human task nexts for %s: %w", rec.ID, err)
		}
	}
	if telegram != "" && telegram != "{}" && telegram != "null" {
		tg := &models.TelegramBinding{}
		if err := json.Unmarshal([]byte(telegram), tg); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal human task telegram for %s: %w", rec.ID, err)
		}
		rec.Telegram = tg
	}
	return rec, nil
}
