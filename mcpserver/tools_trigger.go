package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerTriggerTools exposes the /trigger surface — the inbound webhooks and
// recurring schedules that launch a flow. Reads (list/get) plus the two
// authoring tools (set a webhook, set a schedule). A workflow can carry only one
// trigger, so the set tools create-or-update that single trigger. They reuse the
// same normalization the REST controller does (slug minting, effective-cron,
// auth/whitelist checks, scheduler re-arm), so an MCP-created trigger behaves
// identically to one made in the web app.
func registerTriggerTools(s *server.MCPServer, store repository.Store) {
	repo := store.Triggers()

	s.AddTool(mcp.NewTool("flo_list_triggers",
		withOpts(pageArgs(),
			mcp.WithDescription("List workflow triggers (webhooks and schedules), newest first. Filter by flowId or kind. Secrets are redacted."),
			mcp.WithString("flowId", mcp.Description("only triggers of this workflow")),
			mcp.WithString("kind", mcp.Description("webhook|schedule")),
		)...,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p := listParams(req)
		p.FlowID = strings.TrimSpace(req.GetString("flowId", ""))
		p.Kind = strings.TrimSpace(req.GetString("kind", ""))
		items, total, err := repo.List(ctx, p)
		if err != nil {
			return repoError(err, "triggers not found")
		}
		for i := range items {
			items[i] = presentTrigger(items[i], false)
		}
		return jsonResult(map[string]any{"list": items, "total": total})
	})

	s.AddTool(mcp.NewTool("flo_get_trigger",
		mcp.WithDescription("Get one trigger by id — its config, public URL / next fire, and recent delivery/fire log."),
		mcp.WithString("id", mcp.Required(), mcp.Description("trigger id")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		rec, err := repo.GetByID(ctx, id)
		if err != nil {
			return repoError(err, "trigger not found")
		}
		out := presentTrigger(*rec, false)
		return jsonResult(out)
	})

	s.AddTool(mcp.NewTool("flo_set_webhook_trigger",
		mcp.WithDescription("Create or update the WEBHOOK trigger of a workflow (a flow can have only one trigger). A fire launches the flow when the public URL /hooks/<slug> is called. When auth.method is \"none\" an IP allow-list is required. Returns the trigger with its public URL (secret redacted)."),
		mcp.WithString("id", mcp.Description("trigger id; empty creates a new one")),
		mcp.WithString("flowId", mcp.Required(), mcp.Description("workflow this trigger launches")),
		mcp.WithString("title", mcp.Description("trigger title")),
		mcp.WithBoolean("enabled", mcp.Description("whether the webhook accepts deliveries")),
		mcp.WithString("startNodeId", mcp.Description("entry node; empty runs the flow's start node")),
		mcp.WithString("contextMode", mcp.Description("new (default, fresh context each fire) | existing (reuse one doc)")),
		mcp.WithString("contextId", mcp.Description("required when contextMode=existing")),
		mcp.WithString("contextTitle", mcp.Description("optional title for the doc minted when contextMode=new")),
		mcp.WithString("slug", mcp.Description("public URL slug; empty mints one (kept stable across updates)")),
		mcp.WithArray("methods", mcp.Description("allowed HTTP methods, e.g. [\"POST\"]; empty allows any"), mcp.WithStringItems()),
		mcp.WithArray("whitelistIp", mcp.Description("allowed source IPs; required when auth.method=none"), mcp.WithStringItems()),
		mcp.WithObject("auth", mcp.Description("credential check: {method: none|static|basic|jwt|hmac, secret?, headerKey?, headerPattern?, hashAlgo?, digest?}")),
		mcp.WithObject("settings", mcp.Description("engine overrides: {executeTimeoutSec?, processNodeLimit?, requestTimeoutSec?}")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var t models.Trigger
		if err := req.BindArguments(&t); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid webhook trigger payload", err), nil
		}
		t.Kind = models.TriggerWebhook
		return saveTrigger(ctx, store, &t)
	})

	s.AddTool(mcp.NewTool("flo_set_schedule_trigger",
		mcp.WithDescription("Create or update the SCHEDULE trigger of a workflow (a flow can have only one trigger). When due it starts a fresh run and re-arms. Give either a cron string (mode=cron) or an interval in seconds (mode=interval). Returns the trigger with its next fire time."),
		mcp.WithString("id", mcp.Description("trigger id; empty creates a new one")),
		mcp.WithString("flowId", mcp.Required(), mcp.Description("workflow this trigger launches")),
		mcp.WithString("title", mcp.Description("trigger title")),
		mcp.WithBoolean("enabled", mcp.Description("whether the schedule is armed")),
		mcp.WithString("startNodeId", mcp.Description("entry node; empty runs the flow's start node")),
		mcp.WithString("contextMode", mcp.Description("new (default) | existing")),
		mcp.WithString("contextId", mcp.Description("required when contextMode=existing")),
		mcp.WithString("contextTitle", mcp.Description("optional title for the doc minted when contextMode=new")),
		mcp.WithString("mode", mcp.Required(), mcp.Description("cron | interval")),
		mcp.WithString("cron", mcp.Description("cron spec when mode=cron, e.g. \"0 9 * * 1\"")),
		mcp.WithNumber("intervalSec", mcp.Description("interval in seconds when mode=interval")),
		mcp.WithString("timezone", mcp.Description("IANA timezone for a cron spec (optional)")),
		mcp.WithObject("settings", mcp.Description("engine overrides: {executeTimeoutSec?, processNodeLimit?, requestTimeoutSec?}")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var t models.Trigger
		if err := req.BindArguments(&t); err != nil {
			return mcp.NewToolResultErrorFromErr("invalid schedule trigger payload", err), nil
		}
		t.Kind = models.TriggerSchedule
		return saveTrigger(ctx, store, &t)
	})

	s.AddTool(mcp.NewTool("flo_delete_trigger",
		mcp.WithDescription("Delete a trigger by id (and drop it from the armed schedule set)."),
		mcp.WithString("id", mcp.Required(), mcp.Description("trigger id")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultErrorFromErr("id is required", err), nil
		}
		if err := repo.Delete(ctx, id); err != nil {
			return repoError(err, "trigger not found")
		}
		inflow.NotifyTriggerScheduler()
		return jsonResult(map[string]any{"id": id, "deleted": true})
	})
}

// saveTrigger normalizes, enforces one-trigger-per-flow, upserts, re-arms the
// scheduler for a schedule, and returns the presented (redacted) trigger. It is
// the shared tail of both set tools and mirrors the REST upsert in
// api/trigger/crud.go.
func saveTrigger(ctx context.Context, store repository.Store, t *models.Trigger) (*mcp.CallToolResult, error) {
	if err := normalizeTrigger(ctx, store, t); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := ensureSingleTrigger(ctx, store, t); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := store.Triggers().Upsert(ctx, t); err != nil {
		if strings.Contains(err.Error(), "triggers.slug") {
			return mcp.NewToolResultError("webhook slug already in use"), nil
		}
		return repoError(err, "trigger not found")
	}
	if t.Kind == models.TriggerSchedule {
		inflow.NotifyTriggerScheduler()
	}
	return jsonResult(presentTrigger(*t, false))
}

// normalizeTrigger validates and fills the server-owned fields, mirroring the
// REST controller's normalize (api/trigger/crud.go). Kept in lockstep so an
// MCP-created trigger is identical to a web-app one.
func normalizeTrigger(ctx context.Context, store repository.Store, t *models.Trigger) error {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		t.Title = "untitled trigger"
	}
	if strings.TrimSpace(t.FlowID) == "" {
		return errors.New("flowId is required")
	}

	if t.ContextMode == "" {
		t.ContextMode = models.ContextNew
	}
	switch t.ContextMode {
	case models.ContextExisting:
		t.ContextID = strings.TrimSpace(t.ContextID)
		t.ContextTitle = ""
		if t.Enabled && t.ContextID == "" {
			return errors.New("select a context document (contextId) before enabling this trigger")
		}
	case models.ContextNew:
		t.ContextID = ""
		t.ContextTitle = strings.TrimSpace(t.ContextTitle)
	default:
		return fmt.Errorf("unknown context mode %q", t.ContextMode)
	}
	if s := t.Settings; s != nil && s.ExecuteTimeoutSec == 0 && s.ProcessNodeLimit == 0 && s.RequestTimeoutSec == 0 {
		t.Settings = nil
	}

	switch t.Kind {
	case models.TriggerWebhook:
		if t.Auth == nil {
			t.Auth = &models.WebhookAuth{Method: models.AuthNone}
		}
		if t.Auth.Method == models.AuthNone && len(t.WhitelistIP) == 0 {
			return errors.New("an IP allow-list (whitelistIp) is required when authentication is off")
		}
		if err := ensureTriggerSlug(ctx, store, t); err != nil {
			return err
		}
		for i := range t.Methods {
			t.Methods[i] = strings.ToUpper(strings.TrimSpace(t.Methods[i]))
		}
		t.Mode, t.Cron, t.IntervalSec, t.Timezone, t.CronEffective = "", "", 0, "", ""

	case models.TriggerSchedule:
		spec, err := inflow.EffectiveCron(t)
		if err != nil {
			return err
		}
		t.CronEffective = spec
		t.Slug, t.Methods, t.Auth, t.WhitelistIP = "", nil, nil, nil

	default:
		return fmt.Errorf("unknown trigger kind %q", t.Kind)
	}
	return nil
}

// ensureTriggerSlug keeps a webhook's slug stable across updates and mints one on
// create (mirrors ensureSlug in the REST controller).
func ensureTriggerSlug(ctx context.Context, store repository.Store, t *models.Trigger) error {
	t.Slug = strings.Trim(t.Slug, "/ ")
	if t.Slug != "" {
		return nil
	}
	if t.ID != "" {
		if existing, err := store.Triggers().GetByID(ctx, t.ID); err == nil {
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

// ensureSingleTrigger enforces one trigger per workflow (mirrors the REST
// controller). A read error does not block the save — the DB stays authoritative.
func ensureSingleTrigger(ctx context.Context, store repository.Store, t *models.Trigger) error {
	existing, _, err := store.Triggers().List(ctx, repository.ListParams{FlowID: t.FlowID, Limit: 5})
	if err != nil {
		return nil
	}
	for i := range existing {
		if existing[i].ID != t.ID {
			return errors.New("a workflow can have only one trigger — clone the workflow to add another")
		}
	}
	return nil
}

// presentTrigger computes the read-only fields (webhook public URL, schedule next
// fire) and redacts the secret. reveal is kept false for MCP — a client never
// needs the stored secret echoed back.
func presentTrigger(t models.Trigger, reveal bool) models.Trigger {
	switch t.Kind {
	case models.TriggerWebhook:
		base := strings.TrimRight(env.GetPublicApiUrl(), "/")
		if base == "" {
			t.URL = "/hooks/" + t.Slug
		} else {
			t.URL = base + "/hooks/" + t.Slug
		}
	case models.TriggerSchedule:
		if t.CronEffective != "" {
			if next, err := inflow.NextFire(t.CronEffective, time.Now()); err == nil {
				t.NextAt = next.UnixMilli()
			}
		}
	}
	if !reveal {
		t.Redact()
	}
	return t
}
