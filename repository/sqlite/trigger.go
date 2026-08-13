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

type triggerRepo struct {
	q *sqlcgen.Queries
}

// triggerConfig is the kind-specific portion serialized into the `config`
// column. The webhook fields carry the auth secret (it lives here, off the flow
// graph); the schedule fields carry the recurrence. `cron_effective`, `last_at`
// and `hits` are stored in their own columns, not here.
type triggerConfig struct {
	// run context + settings (both kinds)
	ContextMode  models.TriggerContextMode `json:"contextMode,omitempty"`
	ContextID    string                    `json:"contextId,omitempty"`
	ContextTitle string                    `json:"contextTitle,omitempty"`
	Settings     *models.RunSettings       `json:"settings,omitempty"`
	// webhook
	Methods     []string            `json:"methods,omitempty"`
	Auth        *models.WebhookAuth `json:"auth,omitempty"`
	WhitelistIP []string            `json:"whitelistIp,omitempty"`
	// schedule
	Mode        models.ScheduleMode `json:"mode,omitempty"`
	Cron        string              `json:"cron,omitempty"`
	IntervalSec int64               `json:"intervalSec,omitempty"`
	Timezone    string              `json:"timezone,omitempty"`
}

func (r *triggerRepo) Upsert(ctx context.Context, t *models.Trigger) error {
	now := nowMillis()
	if t.ID == "" {
		t.ID = repository.NewID("trg")
		if t.CreatedAt == 0 {
			t.CreatedAt = now
		}
	} else if existing, err := r.GetByID(ctx, t.ID); err == nil {
		// Update: keep the original creation time and never let a config-only edit
		// clobber runtime state (delivery log, last fire) or drop a stored secret
		// the client left blank (a write-only field it did not resend).
		t.CreatedAt = existing.CreatedAt
		t.LastAt = existing.LastAt
		t.RecentHits = existing.RecentHits
		if t.Auth != nil && t.Auth.Secret == "" && existing.Auth != nil {
			t.Auth.Secret = existing.Auth.Secret
		}
	} else if errors.Is(err, repository.ErrNotFound) {
		if t.CreatedAt == 0 {
			t.CreatedAt = now
		}
	} else {
		return err
	}
	t.UpdatedAt = now

	cfg := triggerConfig{
		ContextMode:  t.ContextMode,
		ContextID:    t.ContextID,
		ContextTitle: t.ContextTitle,
		Settings:     t.Settings,
		Methods:      t.Methods,
		Auth:         t.Auth,
		WhitelistIP:  t.WhitelistIP,
		Mode:         t.Mode,
		Cron:         t.Cron,
		IntervalSec:  t.IntervalSec,
		Timezone:     t.Timezone,
	}
	config, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("sqlite: marshal trigger config: %w", err)
	}
	hits, err := marshalHits(t.RecentHits)
	if err != nil {
		return err
	}
	return r.q.UpsertTrigger(ctx, sqlcgen.UpsertTriggerParams{
		ID:            t.ID,
		Kind:          string(t.Kind),
		FlowID:        t.FlowID,
		StartNodeID:   t.StartNodeID,
		Title:         t.Title,
		Enabled:       boolToInt(t.Enabled),
		Slug:          t.Slug,
		Config:        string(config),
		CronEffective: t.CronEffective,
		LastAt:        t.LastAt,
		Hits:          hits,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
	})
}

func (r *triggerRepo) GetByID(ctx context.Context, id string) (*models.Trigger, error) {
	row, err := r.q.GetTrigger(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return triggerFromRow(row)
}

func (r *triggerRepo) GetBySlug(ctx context.Context, slug string) (*models.Trigger, error) {
	row, err := r.q.GetTriggerBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return triggerFromRow(row)
}

func (r *triggerRepo) List(ctx context.Context, p repository.ListParams) ([]models.Trigger, int64, error) {
	limit := clampLimit(p.Limit)
	total, err := r.q.CountTriggers(ctx, sqlcgen.CountTriggersParams{
		FlowID: p.FlowID,
		Kind:   p.Kind,
		Search: p.Search,
	})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListTriggers(ctx, sqlcgen.ListTriggersParams{
		FlowID: p.FlowID,
		Kind:   p.Kind,
		Search: p.Search,
		Offset: int64(p.Offset),
		Limit:  int64(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.Trigger, 0, len(rows))
	for _, row := range rows {
		rec, err := triggerFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *rec)
	}
	return items, total, nil
}

func (r *triggerRepo) ListEnabledSchedules(ctx context.Context) ([]models.Trigger, error) {
	rows, err := r.q.ListEnabledSchedules(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]models.Trigger, 0, len(rows))
	for _, row := range rows {
		rec, err := triggerFromRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, *rec)
	}
	return items, nil
}

func (r *triggerRepo) RecordHit(ctx context.Context, id string, hit models.TriggerHit) error {
	t, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	hits := append([]models.TriggerHit{hit}, t.RecentHits...)
	if len(hits) > models.MaxTriggerHits {
		hits = hits[:models.MaxTriggerHits]
	}
	blob, err := marshalHits(hits)
	if err != nil {
		return err
	}
	return r.q.UpdateTriggerState(ctx, sqlcgen.UpdateTriggerStateParams{
		LastAt:    t.LastAt,
		Hits:      blob,
		UpdatedAt: nowMillis(),
		ID:        id,
	})
}

func (r *triggerRepo) MarkFired(ctx context.Context, id string, at int64) error {
	t, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	blob, err := marshalHits(t.RecentHits)
	if err != nil {
		return err
	}
	return r.q.UpdateTriggerState(ctx, sqlcgen.UpdateTriggerStateParams{
		LastAt:    at,
		Hits:      blob,
		UpdatedAt: nowMillis(),
		ID:        id,
	})
}

func (r *triggerRepo) Delete(ctx context.Context, id string) error {
	n, err := r.q.DeleteTrigger(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func triggerFromRow(row sqlcgen.Trigger) (*models.Trigger, error) {
	t := &models.Trigger{
		ID:            row.ID,
		Kind:          models.TriggerKind(row.Kind),
		FlowID:        row.FlowID,
		StartNodeID:   row.StartNodeID,
		Title:         row.Title,
		Enabled:       row.Enabled != 0,
		Slug:          row.Slug,
		CronEffective: row.CronEffective,
		LastAt:        row.LastAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	if row.Config != "" {
		var cfg triggerConfig
		if err := json.Unmarshal([]byte(row.Config), &cfg); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal trigger config for %s: %w", row.ID, err)
		}
		t.ContextMode = cfg.ContextMode
		t.ContextID = cfg.ContextID
		t.ContextTitle = cfg.ContextTitle
		t.Settings = cfg.Settings
		t.Methods = cfg.Methods
		t.Auth = cfg.Auth
		t.WhitelistIP = cfg.WhitelistIP
		t.Mode = cfg.Mode
		t.Cron = cfg.Cron
		t.IntervalSec = cfg.IntervalSec
		t.Timezone = cfg.Timezone
	}
	if row.Hits != "" {
		if err := json.Unmarshal([]byte(row.Hits), &t.RecentHits); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal trigger hits for %s: %w", row.ID, err)
		}
	}
	return t, nil
}

func marshalHits(hits []models.TriggerHit) (string, error) {
	if len(hits) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(hits)
	if err != nil {
		return "", fmt.Errorf("sqlite: marshal trigger hits: %w", err)
	}
	return string(b), nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
