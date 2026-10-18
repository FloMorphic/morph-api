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
	triggerColumns = `id, kind, flow_id, start_node_id, title, enabled, slug, config,
	cron_effective, last_at, hits, created_at, updated_at`

	triggerUpsertSQL = `
INSERT INTO triggers (
    id, kind, flow_id, start_node_id, title, enabled, slug, config,
    cron_effective, last_at, hits, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (id) DO UPDATE SET
    kind = EXCLUDED.kind,
    flow_id = EXCLUDED.flow_id,
    start_node_id = EXCLUDED.start_node_id,
    title = EXCLUDED.title,
    enabled = EXCLUDED.enabled,
    slug = EXCLUDED.slug,
    config = EXCLUDED.config,
    cron_effective = EXCLUDED.cron_effective,
    last_at = EXCLUDED.last_at,
    hits = EXCLUDED.hits,
    updated_at = EXCLUDED.updated_at`

	// $1 flow, $2 kind, $3 search — the flow editor lists one flow's triggers.
	triggerFilterSQL = `($1::text = '' OR flow_id = $1::text)
	  AND ($2::text = '' OR kind = $2::text)
	  AND ($3::text = '' OR title ILIKE '%' || $3::text || '%')`
)

// triggerConfig is the kind-specific portion serialized into the `config`
// column. The webhook fields carry the auth secret (it lives here, off the flow
// graph); the schedule fields carry the recurrence. `cron_effective`, `last_at`
// and `hits` are stored in their own columns, not here.
//
// It is deliberately the same shape as the sqlite backend's: the column holds
// one JSON document either backend can read, so a database handed from one to
// the other needs no translation.
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

type triggerRepo struct {
	db *sql.DB
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
		return fmt.Errorf("postgres: marshal trigger config: %w", err)
	}
	hits, err := marshalHits(t.RecentHits)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, triggerUpsertSQL,
		t.ID, string(t.Kind), t.FlowID, t.StartNodeID, t.Title, t.Enabled, t.Slug,
		string(config), t.CronEffective, t.LastAt, hits, t.CreatedAt, t.UpdatedAt)
	return err
}

func (r *triggerRepo) GetByID(ctx context.Context, id string) (*models.Trigger, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+triggerColumns+` FROM triggers WHERE id = $1`, id)
	return scanTrigger(row)
}

// GetBySlug resolves an enabled webhook by its public slug — the ingress lookup.
// A disabled trigger is reported as not found: from a caller's point of view the
// hook does not exist.
func (r *triggerRepo) GetBySlug(ctx context.Context, slug string) (*models.Trigger, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+triggerColumns+` FROM triggers WHERE slug = $1 AND enabled = TRUE`, slug)
	return scanTrigger(row)
}

func (r *triggerRepo) List(ctx context.Context, p repository.ListParams) ([]models.Trigger, int64, error) {
	limit := clampLimit(p.Limit)
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM triggers WHERE `+triggerFilterSQL,
		p.FlowID, p.Kind, p.Search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+triggerColumns+` FROM triggers WHERE `+triggerFilterSQL+`
		 ORDER BY updated_at DESC, id DESC LIMIT $4 OFFSET $5`,
		p.FlowID, p.Kind, p.Search, limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := collectTriggers(rows, limit)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListEnabledSchedules returns every enabled schedule trigger — the set the
// recurring scheduler arms its timer from.
func (r *triggerRepo) ListEnabledSchedules(ctx context.Context) ([]models.Trigger, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+triggerColumns+` FROM triggers WHERE kind = 'schedule' AND enabled = TRUE ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectTriggers(rows, 16)
}

// RecordHit prepends a delivery to the webhook's bounded log. It writes only the
// state columns, so a concurrent config edit cannot be clobbered by a delivery
// (and vice versa).
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
	return r.updateState(ctx, id, t.LastAt, blob)
}

// MarkFired stamps a schedule trigger's last-fire time.
func (r *triggerRepo) MarkFired(ctx context.Context, id string, at int64) error {
	t, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	blob, err := marshalHits(t.RecentHits)
	if err != nil {
		return err
	}
	return r.updateState(ctx, id, at, blob)
}

func (r *triggerRepo) updateState(ctx context.Context, id string, lastAt int64, hits string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE triggers SET last_at = $1, hits = $2, updated_at = $3 WHERE id = $4`,
		lastAt, hits, nowMillis(), id)
	return err
}

func (r *triggerRepo) Delete(ctx context.Context, id string) error {
	return execExpectRow(ctx, r.db, `DELETE FROM triggers WHERE id = $1`, id)
}

func collectTriggers(rows *sql.Rows, capacity int) ([]models.Trigger, error) {
	items := make([]models.Trigger, 0, capacity)
	for rows.Next() {
		rec, err := scanTrigger(rows)
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

func scanTrigger(row scanner) (*models.Trigger, error) {
	var (
		t            models.Trigger
		kind         string
		config, hits string
	)
	if err := row.Scan(&t.ID, &kind, &t.FlowID, &t.StartNodeID, &t.Title, &t.Enabled, &t.Slug,
		&config, &t.CronEffective, &t.LastAt, &hits, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	t.Kind = models.TriggerKind(kind)
	if config != "" {
		var cfg triggerConfig
		if err := json.Unmarshal([]byte(config), &cfg); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal trigger config for %s: %w", t.ID, err)
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
	if hits != "" {
		if err := json.Unmarshal([]byte(hits), &t.RecentHits); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal trigger hits for %s: %w", t.ID, err)
		}
	}
	return &t, nil
}

func marshalHits(hits []models.TriggerHit) (string, error) {
	if len(hits) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(hits)
	if err != nil {
		return "", fmt.Errorf("postgres: marshal trigger hits: %w", err)
	}
	return string(b), nil
}
