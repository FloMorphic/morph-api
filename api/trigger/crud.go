package triggerControllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

// upsert handles POST /trigger — create (no id) or update (existing id) a
// webhook or schedule trigger.
func (ctl *controller) upsert(c fiber.Ctx) error {
	var in models.Trigger
	if err := c.Bind().Body(&in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid trigger payload")
	}
	if err := ctl.normalize(c.Context(), &in); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	if err := ctl.ensureSingleTrigger(c.Context(), &in); err != nil {
		return etc.Fail(c, fiber.StatusConflict, err.Error())
	}
	if err := ctl.store.Triggers().Upsert(c.Context(), &in); err != nil {
		if isSlugConflict(err) {
			return etc.Fail(c, fiber.StatusConflict, "webhook slug already in use")
		}
		return etc.FailFromRepo(c, err, "trigger not found")
	}
	// A saved/changed schedule may now be the nearest fire — re-arm the loop.
	if in.Kind == models.TriggerSchedule {
		inflow.NotifyTriggerScheduler()
	}
	return etc.OK(c, ctl.present(&in, false))
}

// list handles GET /trigger — page-based listing scoped by flow and/or kind.
func (ctl *controller) list(c fiber.Ctx) error {
	var q models.PaginationParams
	if err := c.Bind().Query(&q); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	if err := q.Normalize(); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, err.Error())
	}
	items, total, err := ctl.store.Triggers().List(c.Context(), repository.ListParams{
		Offset: q.Offset(),
		Limit:  q.PerPage,
		Search: q.Search,
		FlowID: strings.TrimSpace(c.Query("flowId")),
		Kind:   strings.TrimSpace(c.Query("kind")),
	})
	if err != nil {
		return etc.FailFromRepo(c, err, "triggers not found")
	}
	presented := make([]models.Trigger, len(items))
	for i := range items {
		presented[i] = ctl.present(&items[i], false)
	}
	return etc.OK(c, models.NewPage(presented, total, q))
}

// getByID handles GET /trigger/id/:id. This single-record read reveals the
// webhook secret (unlike the list) so the drawer can open its settings with the
// stored secret loaded — masked in the UI until the user chooses to see it.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.store.Triggers().GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "trigger not found")
	}
	return etc.OK(c, ctl.present(rec, true))
}

// deleteByID handles DELETE /trigger/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.store.Triggers().Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "trigger not found")
	}
	// If it was a schedule, drop it from the armed set (a cheap no-op otherwise).
	inflow.NotifyTriggerScheduler()
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}

// normalize validates the trigger and fills the derived, server-owned fields:
// a webhook gets a unique slug (kept across updates) and its methods upper-cased
// with the schedule fields cleared; a schedule gets its single effective cron
// spec computed (interval → `@every`) with the webhook fields cleared.
func (ctl *controller) normalize(ctx context.Context, t *models.Trigger) error {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		t.Title = "untitled trigger"
	}
	if strings.TrimSpace(t.FlowID) == "" {
		return errors.New("flowId is required")
	}

	// Run context (both kinds). A fire starts a process, which needs a doc — so an
	// enabled trigger must resolve to one: `existing` mode has to name a doc,
	// `new` mints one per fire and is always usable.
	if t.ContextMode == "" {
		t.ContextMode = models.ContextNew
	}
	switch t.ContextMode {
	case models.ContextExisting:
		t.ContextID = strings.TrimSpace(t.ContextID)
		t.ContextTitle = ""
		if t.Enabled && t.ContextID == "" {
			return errors.New("select a context document before enabling this trigger")
		}
	case models.ContextNew:
		t.ContextID = ""
		t.ContextTitle = strings.TrimSpace(t.ContextTitle)
	default:
		return fmt.Errorf("unknown context mode %q", t.ContextMode)
	}
	if s := t.Settings; s != nil && s.ExecuteTimeoutSec == 0 && s.ProcessNodeLimit == 0 && s.RequestTimeoutSec == 0 {
		t.Settings = nil // all-default settings carry nothing
	}

	switch t.Kind {
	case models.TriggerWebhook:
		if t.Auth == nil {
			t.Auth = &models.WebhookAuth{Method: models.AuthNone}
		}
		if t.Auth.Method == models.AuthNone && len(t.WhitelistIP) == 0 {
			return errors.New("an IP allow-list is required when authentication is off")
		}
		if err := ctl.ensureSlug(ctx, t); err != nil {
			return err
		}
		for i := range t.Methods {
			t.Methods[i] = strings.ToUpper(strings.TrimSpace(t.Methods[i]))
		}
		// Clear schedule-only fields so a kind switch leaves nothing stale.
		t.Mode, t.Cron, t.IntervalSec, t.Timezone, t.CronEffective = "", "", 0, "", ""

	case models.TriggerSchedule:
		spec, err := inflow.EffectiveCron(t)
		if err != nil {
			return err
		}
		t.CronEffective = spec
		// Clear webhook-only fields.
		t.Slug, t.Methods, t.Auth, t.WhitelistIP = "", nil, nil, nil

	default:
		return fmt.Errorf("unknown trigger kind %q", t.Kind)
	}
	return nil
}

// ensureSingleTrigger enforces one trigger per workflow: a fire always launches
// this flow, and wanting several entry points is better modeled as separate
// (cloned) workflows. A save is allowed only when the flow has no other trigger.
func (ctl *controller) ensureSingleTrigger(ctx context.Context, t *models.Trigger) error {
	existing, _, err := ctl.store.Triggers().List(ctx, repository.ListParams{FlowID: t.FlowID, Limit: 5})
	if err != nil {
		return nil // don't block a save on a read error; the DB stays the authority
	}
	for i := range existing {
		if existing[i].ID != t.ID {
			return errors.New("a workflow can have only one trigger — clone the workflow to add another")
		}
	}
	return nil
}

// ensureSlug assigns a webhook slug when none is given: it keeps the existing
// slug on an update (so the public URL is stable), and mints a fresh URL-safe id
// on a create.
func (ctl *controller) ensureSlug(ctx context.Context, t *models.Trigger) error {
	t.Slug = strings.Trim(t.Slug, "/ ")
	if t.Slug != "" {
		return nil
	}
	if t.ID != "" {
		if existing, err := ctl.store.Triggers().GetByID(ctx, t.ID); err == nil {
			t.Slug = existing.Slug
		} else if !repository.IsNotFound(err) {
			return err
		}
	}
	if t.Slug == "" {
		t.Slug = repository.NewID("")
	}
	return nil
}

// present prepares a trigger for the wire: it computes the read-only fields
// (public URL for a webhook, next fire for a schedule). When reveal is false the
// secret is redacted (list, upsert echo); when true it is kept so a single-record
// read can hand the drawer the stored secret to display.
func (ctl *controller) present(t *models.Trigger, reveal bool) models.Trigger {
	out := *t
	switch out.Kind {
	case models.TriggerWebhook:
		out.URL = webhookURL(out.Slug)
	case models.TriggerSchedule:
		if out.CronEffective != "" {
			if next, err := inflow.NextFire(out.CronEffective, time.Now()); err == nil {
				out.NextAt = next.UnixMilli()
			}
		}
	}
	if reveal {
		if out.Auth != nil {
			out.HasSecret = out.Auth.Secret != ""
		}
	} else {
		out.Redact()
	}
	return out
}

// webhookURL builds the fully-qualified ingress URL from the configured public
// base, falling back to a root-relative path when none is set.
func webhookURL(slug string) string {
	base := strings.TrimRight(env.GetPublicApiUrl(), "/")
	if base == "" {
		return "/hooks/" + slug
	}
	return base + "/hooks/" + slug
}

// isSlugConflict reports whether a repo error is the unique-slug violation.
func isSlugConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "triggers.slug")
}
