package inflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/bytedance/sonic"
	"github.com/robfig/cron/v3"
)

// cronParser accepts both standard 5-field cron and descriptors (@every, @daily,
// …) — exactly the two shapes an effective schedule spec can take. A CRON_TZ=
// prefix (added by EffectiveCron for timezone-bound crons) is handled natively.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// EffectiveCron reduces a schedule trigger's two authoring modes to the single
// spec the scheduler arms on — the whole point of normalizing here is that the
// scheduler then has ONE code path. A cron string stays a cron (tagged with its
// timezone when set); an interval becomes an `@every <duration>` descriptor. The
// result is validated by parsing it, so a bad cron or a non-positive interval is
// rejected at save time.
func EffectiveCron(t *models.Trigger) (string, error) {
	var spec string
	switch t.Mode {
	case models.ScheduleCron:
		spec = strings.TrimSpace(t.Cron)
		if spec == "" {
			return "", errors.New("cron expression is required")
		}
		if t.Timezone != "" && !strings.HasPrefix(spec, "CRON_TZ=") && !strings.HasPrefix(spec, "@") {
			spec = "CRON_TZ=" + t.Timezone + " " + spec
		}
	case models.ScheduleInterval:
		if t.IntervalSec <= 0 {
			return "", errors.New("interval must be greater than zero")
		}
		spec = "@every " + (time.Duration(t.IntervalSec) * time.Second).String()
	default:
		return "", fmt.Errorf("unknown schedule mode %q", t.Mode)
	}
	if _, err := cronParser.Parse(spec); err != nil {
		return "", fmt.Errorf("invalid schedule: %w", err)
	}
	return spec, nil
}

// NextFire returns the first time the spec fires strictly after `after`, or a
// zero time and error when the spec does not parse.
func NextFire(spec string, after time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(spec)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(after), nil
}

// LaunchTrigger starts a run of a trigger's flow, resolving its context document
// per the trigger's ContextMode, then dispatching through StartWorkflow at the
// bound entry node (empty resolves the flow's own start node) with the trigger's
// run settings. Returns the launched process.
//
//   - ContextExisting: use the selected doc directly (the run mutates it in
//     place). A webhook's payload is merged into that doc so the flow sees it.
//   - ContextNew (default): mint a fresh context each fire — a webhook's payload
//     becomes its body, a schedule's is an empty object — so runs stay isolated.
//
// This is the single launch path shared by the webhook ingress and the recurring
// scheduler, so both produce identical run shapes (a run tagged with its trigger
// id in the engine meta). `payload` is nil for a schedule fire.
func LaunchTrigger(ctx context.Context, store repository.Store, t *models.Trigger, ctxTitle string, payload map[string]any) (*models.Process, error) {
	contextID, err := resolveTriggerContext(ctx, store, t, ctxTitle, payload)
	if err != nil {
		return nil, err
	}
	return StartWorkflow(ctx, store, StartParams{
		FlowID:       t.FlowID,
		ContextID:    contextID,
		StartNodeIDs: []string{t.StartNodeID},
		ReqMeta:      map[string]string{"trigger": t.ID},
		Settings:     triggerRunSettings(t),
	})
}

// resolveTriggerContext returns the context document id a fire should run
// against, following the trigger's ContextMode (see LaunchTrigger).
func resolveTriggerContext(ctx context.Context, store repository.Store, t *models.Trigger, ctxTitle string, payload map[string]any) (string, error) {
	if t.ContextMode == models.ContextExisting {
		if t.ContextID == "" {
			return "", errors.New("trigger has no context document selected")
		}
		if payload != nil {
			// Webhook against a reused doc: fold the request into it so the flow
			// reads the same payload keys it would on a fresh context.
			if err := mergePayloadIntoContext(ctx, store, t.ContextID, payload); err != nil {
				return "", err
			}
		}
		return t.ContextID, nil
	}

	// ContextNew (and any legacy empty mode): create a fresh context at fire time,
	// one path for both kinds. A webhook's payload is its content; a schedule
	// starts from an empty object. Creating it here (rather than leaning on the
	// wire's lazy create-on-miss) lets us title it with the user's ContextTitle
	// and surface the row the moment the run starts — and for `new` mode the row
	// is always needed, so there is nothing to defer. (The wire's create-on-miss
	// remains a safety net for any run launched against an unwritten id.)
	body := "{}"
	if payload != nil {
		b, err := sonic.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("marshal trigger payload: %w", err)
		}
		body = string(b)
	}
	title := ctxTitle
	if t.ContextTitle != "" {
		title = t.ContextTitle
	}
	rec := &models.ContextRecord{
		Title:     title,
		Context:   body,
		UpdatedBy: models.LastChange{By: models.ByFlow},
	}
	if err := store.Contexts().Upsert(ctx, rec); err != nil {
		return "", fmt.Errorf("create trigger context: %w", err)
	}
	return rec.ID, nil
}

// mergePayloadIntoContext spreads a webhook payload's top-level keys into an
// existing context document, preserving the doc's other keys.
func mergePayloadIntoContext(ctx context.Context, store repository.Store, contextID string, payload map[string]any) error {
	rec, err := store.Contexts().GetByID(ctx, contextID)
	if err != nil {
		return fmt.Errorf("load trigger context %s: %w", contextID, err)
	}
	doc := map[string]any{}
	if rec.Context != "" {
		if err := sonic.Unmarshal([]byte(rec.Context), &doc); err != nil {
			// A non-object context can't be merged into; overwrite it wholesale.
			doc = map[string]any{}
		}
	}
	for k, v := range payload {
		doc[k] = v
	}
	merged, err := sonic.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal merged context: %w", err)
	}
	rec.Context = string(merged)
	rec.UpdatedBy = models.LastChange{By: models.ByFlow}
	return store.Contexts().Upsert(ctx, rec)
}

// triggerRunSettings maps a trigger's saved run settings to the engine override
// struct; nil settings yield a zero value (all engine defaults kept).
func triggerRunSettings(t *models.Trigger) RunSettings {
	if t.Settings == nil {
		return RunSettings{}
	}
	return RunSettings{
		ExecuteTimeoutSec: t.Settings.ExecuteTimeoutSec,
		ProcessNodeLimit:  t.Settings.ProcessNodeLimit,
		RequestTimeoutSec: t.Settings.RequestTimeoutSec,
	}
}
