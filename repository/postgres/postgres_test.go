package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/repository"
	compiler "github.com/Inflowenger/inflow-fusion/compilers/vueFlow"
)

// These tests need a real server — every statement here is hand-written SQL, so
// the only meaningful check is that postgres accepts it and round-trips the
// entities. Point TEST_POSTGRES_DSN at a scratch database to run them:
//
//	TEST_POSTGRES_DSN='postgres://postgres:pass@localhost:5432/flomorphic_test?sslmode=disable' go test ./repository/postgres/
//
// Without it they skip, so the suite stays runnable with no infrastructure.
func openTestStore(t *testing.T) repository.Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the postgres repository tests")
	}
	// Vector memory is sqlite-vec even here; give each run its own sidecar file.
	SetVectorSource(filepath.Join(t.TempDir(), "vectors.db"))
	t.Cleanup(func() { SetVectorSource("") })

	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Workflows exercise the shape every CRUD entity shares: an upsert that keeps
// the original CreatedAt, a case-insensitive search, offset paging, and a delete
// that reports ErrNotFound when it matched nothing.
func TestWorkflowCRUD(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Workflows()

	rec := &models.FlowRecord{Title: "Daily Report"}
	if err := repo.Upsert(ctx, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if rec.ID == "" || rec.CreatedAt == 0 || rec.UpdatedAt == 0 {
		t.Fatalf("insert should assign id and timestamps: %+v", rec)
	}
	createdAt := rec.CreatedAt
	t.Cleanup(func() { _ = repo.Delete(ctx, rec.ID) })

	rec.Title = "Daily Report v2"
	if err := repo.Upsert(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}
	if rec.CreatedAt != createdAt {
		t.Fatalf("update rewrote CreatedAt: %d -> %d", createdAt, rec.CreatedAt)
	}

	got, err := repo.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Daily Report v2" {
		t.Fatalf("round-trip lost the title: %+v", got)
	}

	// Search is case-insensitive (sqlite's LIKE is, so postgres uses ILIKE).
	items, total, err := repo.List(ctx, repository.ListParams{Search: "daily report v2", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total == 0 || len(items) == 0 {
		t.Fatalf("case-insensitive search found nothing (total %d)", total)
	}
	// A page past the end is empty but still reports the full total.
	items, total, err = repo.List(ctx, repository.ListParams{Search: "daily report v2", Limit: 10, Offset: 1000})
	if err != nil {
		t.Fatalf("paged search: %v", err)
	}
	if len(items) != 0 || total == 0 {
		t.Fatalf("offset past the end: got %d items, total %d", len(items), total)
	}

	if err := repo.Delete(ctx, rec.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.Delete(ctx, rec.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleting twice should be ErrNotFound, got %v", err)
	}
	if _, err := repo.GetByID(ctx, rec.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("get after delete should be ErrNotFound, got %v", err)
	}
}

// Contexts carry a nested header object and a change attribution, and the
// context body is stored verbatim as the caller's JSON text.
func TestContextCRUD(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Contexts()

	rec := &models.ContextRecord{
		Title:     "Run context",
		Context:   `{"customer":"acme"}`,
		Header:    map[string]any{"trace": "abc"},
		UpdatedBy: models.LastChange{By: models.ByAPI, Address: "127.0.0.1"},
	}
	if err := repo.Upsert(ctx, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, rec.ID) })

	got, err := repo.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Context != `{"customer":"acme"}` {
		t.Fatalf("context body changed: %q", got.Context)
	}
	if got.Header["trace"] != "abc" {
		t.Fatalf("header lost: %+v", got.Header)
	}
	if got.UpdatedBy.By != models.ByAPI || got.UpdatedBy.Address != "127.0.0.1" {
		t.Fatalf("attribution lost: %+v", got.UpdatedBy)
	}
}

// The starter prompts are seeded on open; seeding again must insert nothing and
// must not overwrite an edit.
func TestPromptSeedingIsIdempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Prompts()

	items, _, err := repo.List(ctx, repository.ListParams{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("open should have seeded the starter prompts")
	}
	first := items[0]
	first.Title = "edited by an admin"
	if err := repo.Upsert(ctx, &first); err != nil {
		t.Fatalf("edit: %v", err)
	}

	n, err := repo.SeedPrompts(ctx, []models.PromptRecord{{ID: first.ID, Title: "from the seed"}})
	if err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-seeding an existing prompt inserted %d rows", n)
	}
	got, err := repo.GetByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "edited by an admin" {
		t.Fatalf("re-seeding clobbered an edit: %q", got.Title)
	}
	// Restore, so a shared scratch database stays recognisable between runs.
	got.Title = items[0].Title
	_ = repo.Upsert(ctx, got)
}

// Node settings are listed with an optional node filter — what the node drawer
// fetches.
func TestNodeSettingsNodeFilter(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.NodeSettings()

	mine := &models.NodeSetting{NodeUniqID: "test-node-a", Title: "prod", Settings: map[string]any{"url": "https://a"}}
	other := &models.NodeSetting{NodeUniqID: "test-node-b", Title: "prod", Settings: map[string]any{"url": "https://b"}}
	for _, s := range []*models.NodeSetting{mine, other} {
		if err := repo.Upsert(ctx, s); err != nil {
			t.Fatalf("insert %s: %v", s.NodeUniqID, err)
		}
		rec := s
		t.Cleanup(func() { _ = repo.Delete(ctx, rec.ID) })
	}

	items, total, err := repo.List(ctx, repository.ListParams{NodeUniqID: "test-node-a", Limit: 10})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != mine.ID {
		t.Fatalf("filter returned %d rows (total %d), want just %s", len(items), total, mine.ID)
	}
	if items[0].Settings["url"] != "https://a" {
		t.Fatalf("settings lost: %+v", items[0].Settings)
	}
}

// A human task's answers flip its status once every question is answered, and
// closing it stamps ClosedAt. The JSON collections and the telegram binding have
// to survive the round trip.
func TestHumanTaskLifecycle(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.HumanTasks()

	task := &models.HumanTask{
		Title:  "Approve the invoice",
		FlowID: "flow_test",
		Questions: []models.HumanTaskQuestion{
			{ID: "q1", Text: "Approve?"},
			{ID: "q2", Text: "Why?"},
		},
		Data:     map[string]any{"amount": 42.0},
		Telegram: &models.TelegramBinding{Connection: "cnx_1", Alias: "bot", ChatID: "99"},
	}
	if err := repo.Upsert(ctx, task); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, task.ID) })

	got, err := repo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.HumanTaskOpen || got.Mode != models.HumanTaskPark || got.Channel != models.HumanTaskDirect {
		t.Fatalf("defaults not applied: %+v", got)
	}
	if got.Telegram == nil || got.Telegram.ChatID != "99" {
		t.Fatalf("telegram binding lost: %+v", got.Telegram)
	}
	if got.Data["amount"] != 42.0 {
		t.Fatalf("data snapshot lost: %+v", got.Data)
	}

	after, err := repo.Answer(ctx, task.ID, "q1", "yes")
	if err != nil {
		t.Fatalf("answer q1: %v", err)
	}
	if after.Status != models.HumanTaskOpen {
		t.Fatalf("status after one of two answers is %q, want open", after.Status)
	}
	after, err = repo.Answer(ctx, task.ID, "q2", "because")
	if err != nil {
		t.Fatalf("answer q2: %v", err)
	}
	if after.Status != models.HumanTaskAnswered {
		t.Fatalf("status after every answer is %q, want answered", after.Status)
	}

	if _, err := repo.AppendMessage(ctx, task.ID, models.HumanTaskMessage{Text: "thanks"}); err != nil {
		t.Fatalf("append message: %v", err)
	}
	closed, err := repo.Close(ctx, task.ID)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Status != models.HumanTaskClosed || closed.ClosedAt == 0 {
		t.Fatalf("close did not finish the task: %+v", closed)
	}
	if len(closed.Messages) != 1 {
		t.Fatalf("messages lost: %+v", closed.Messages)
	}

	// The status and flow filters are what the HITL drawer lists by.
	items, _, err := repo.List(ctx, repository.ListParams{Status: string(models.HumanTaskClosed), FlowID: "flow_test", Limit: 10})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("status+flow filter found nothing")
	}
}

// The palette is seeded on open, re-seeding inserts nothing, and a plugin's
// action rows are listed and dropped as a set without touching its registration
// row.
func TestExtensionSeedAndPluginActions(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Extensions()

	builtins, total, err := repo.List(ctx, repository.ListParams{Kind: string(models.KindBuiltin), Limit: 100})
	if err != nil {
		t.Fatalf("list builtins: %v", err)
	}
	if total == 0 || len(builtins) == 0 {
		t.Fatalf("open should have seeded the builtin palette")
	}
	// Re-seeding the same definitions must be a no-op.
	n, err := repo.SeedBuiltins(ctx, []models.ExtensionRecord{{Name: builtins[0].Name}})
	if err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-seeding an existing builtin inserted %d rows", n)
	}

	const pluginID = "test-plugin-residency"
	registration := &models.ExtensionRecord{Name: "Test Plugin", PluginID: pluginID}
	actionA := &models.ExtensionRecord{Name: "Test Plugin: A", PluginID: pluginID, Action: "a"}
	actionB := &models.ExtensionRecord{Name: "Test Plugin: B", PluginID: pluginID, Action: "b"}
	for _, e := range []*models.ExtensionRecord{registration, actionA, actionB} {
		if err := repo.Upsert(ctx, e); err != nil {
			t.Fatalf("insert %s: %v", e.Name, err)
		}
		rec := e
		t.Cleanup(func() { _ = repo.Delete(ctx, rec.ID) })
	}

	actions, err := repo.ListPluginActions(ctx, pluginID)
	if err != nil {
		t.Fatalf("list plugin actions: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("expected 2 action rows, got %d", len(actions))
	}
	dropped, err := repo.DeletePluginActions(ctx, pluginID)
	if err != nil {
		t.Fatalf("delete plugin actions: %v", err)
	}
	if dropped != 2 {
		t.Fatalf("dropped %d action rows, want 2", dropped)
	}
	if _, err := repo.GetByID(ctx, registration.ID); err != nil {
		t.Fatalf("the registration row must survive: %v", err)
	}
}

// Triggers: the slug lookup only resolves an enabled webhook, the schedule list
// is the scheduler's queue, and the state writers must not touch the config
// (notably the stored secret).
func TestTriggerLookupsAndState(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Triggers()

	hook := &models.Trigger{
		Kind:    models.TriggerWebhook,
		FlowID:  "flow_test",
		Title:   "inbound",
		Enabled: true,
		Slug:    "test-hook-" + repository.NewID("s"),
		Methods: []string{"POST"},
		Auth:    &models.WebhookAuth{Secret: "s3cret"},
	}
	if err := repo.Upsert(ctx, hook); err != nil {
		t.Fatalf("insert webhook: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, hook.ID) })

	got, err := repo.GetBySlug(ctx, hook.Slug)
	if err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if got.Auth == nil || got.Auth.Secret != "s3cret" {
		t.Fatalf("webhook secret lost: %+v", got.Auth)
	}

	// An edit that leaves the secret blank keeps the stored one.
	hook.Auth = &models.WebhookAuth{}
	hook.Title = "inbound v2"
	if err := repo.Upsert(ctx, hook); err != nil {
		t.Fatalf("edit webhook: %v", err)
	}
	if got, err = repo.GetByID(ctx, hook.ID); err != nil {
		t.Fatalf("get: %v", err)
	} else if got.Auth.Secret != "s3cret" {
		t.Fatalf("blank secret clobbered the stored one: %q", got.Auth.Secret)
	}

	// Recording a delivery keeps the config intact and bounds the log.
	if err := repo.RecordHit(ctx, hook.ID, models.TriggerHit{At: 1, Status: 200}); err != nil {
		t.Fatalf("record hit: %v", err)
	}
	if got, err = repo.GetByID(ctx, hook.ID); err != nil {
		t.Fatalf("get after hit: %v", err)
	}
	if len(got.RecentHits) != 1 || got.Auth.Secret != "s3cret" {
		t.Fatalf("hit log/secret wrong after RecordHit: %d hits, secret %q", len(got.RecentHits), got.Auth.Secret)
	}

	// A disabled webhook is not resolvable by slug — from a caller's point of
	// view the hook does not exist.
	got.Enabled = false
	if err := repo.Upsert(ctx, got); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := repo.GetBySlug(ctx, hook.Slug); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("a disabled hook resolved by slug: %v", err)
	}

	sched := &models.Trigger{
		Kind: models.TriggerSchedule, FlowID: "flow_test", Title: "nightly",
		Enabled: true, Mode: models.ScheduleCron, Cron: "0 3 * * *", CronEffective: "0 3 * * *",
	}
	if err := repo.Upsert(ctx, sched); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, sched.ID) })

	schedules, err := repo.ListEnabledSchedules(ctx)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	found := false
	for _, s := range schedules {
		if s.ID == sched.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the enabled schedule is not in the scheduler's list")
	}
	if err := repo.MarkFired(ctx, sched.ID, 1234); err != nil {
		t.Fatalf("mark fired: %v", err)
	}
	if got, err := repo.GetByID(ctx, sched.ID); err != nil {
		t.Fatalf("get schedule: %v", err)
	} else if got.LastAt != 1234 || got.Cron != "0 3 * * *" {
		t.Fatalf("MarkFired wrote the wrong thing: lastAt %d, cron %q", got.LastAt, got.Cron)
	}
}

// A process row's identity is its auto-increment index, which the launch path
// needs back immediately; the scheduler's queue reads are indexed on status.
func TestProcessCreateUpdateAndScheduling(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Processes()

	pid := repository.NewID("pid")
	run := &models.Process{PID: pid, FlowID: "flow_test", Status: models.ProcessRunning,
		Request: map[string]any{"flowId": "flow_test"}}
	if err := repo.Create(ctx, run); err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.IndexID == 0 {
		t.Fatalf("create must populate IndexID")
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, run.IndexID) })

	running, err := repo.GetRunningByPID(ctx, pid)
	if err != nil {
		t.Fatalf("get running by pid: %v", err)
	}
	if running.IndexID != run.IndexID {
		t.Fatalf("resolved index %d, want %d", running.IndexID, run.IndexID)
	}

	run.Status = models.ProcessFinished
	run.Snapshot = map[string]any{"traverse": []any{"n1"}}
	run.Errors = map[string]any{"count": 0.0}
	if err := repo.Update(ctx, run); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := repo.GetByIndex(ctx, run.IndexID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.ProcessFinished || got.Snapshot == nil || got.Errors == nil {
		t.Fatalf("update lost data: %+v", got)
	}
	if _, err := repo.GetRunningByPID(ctx, pid); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("a finished run should not resolve as running: %v", err)
	}
	if err := repo.Update(ctx, &models.Process{IndexID: 999999999, PID: "nope"}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("updating a missing row should be ErrNotFound, got %v", err)
	}

	// The scheduled queue: the nearest row is the head, and a due batch includes
	// it once its time has passed.
	soon := &models.Process{PID: repository.NewID("pid"), FlowID: "flow_test",
		Status: models.ProcessScheduled, ScheduledAt: 1}
	if err := repo.Create(ctx, soon); err != nil {
		t.Fatalf("create scheduled: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, soon.IndexID) })

	next, err := repo.NextScheduled(ctx)
	if err != nil {
		t.Fatalf("next scheduled: %v", err)
	}
	if next.ScheduledAt != 1 {
		t.Fatalf("the nearest scheduled row is at %d, want 1", next.ScheduledAt)
	}
	due, err := repo.ListDueScheduled(ctx, 2)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) == 0 {
		t.Fatalf("a row scheduled in the past is not due")
	}

	// The pid/flow filters are what the runs table lists by.
	items, total, err := repo.List(ctx, repository.ListParams{PID: pid, Limit: 10})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("pid filter returned %d rows (total %d), want 1", len(items), total)
	}
}

// The Connect gateways keep a single default as rows come and go — the same
// contract the sqlite backend is tested for.
func TestConnectDefaultLifecycle(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.Connect()

	// Start from a known state: this entity is an unpaginated set with one
	// default, so leftovers from another run would make the assertions lie.
	existing, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range existing {
		if err := repo.Delete(ctx, c.ID); err != nil {
			t.Fatalf("clear %s: %v", c.ID, err)
		}
	}

	a := &models.ConnectConnection{Label: "A", BaseURL: "https://a", Token: "ta"}
	if err := repo.Upsert(ctx, a); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, a.ID) })
	if !a.IsDefault {
		t.Fatalf("the first connection should be default")
	}

	b := &models.ConnectConnection{Label: "B", BaseURL: "https://b", Token: "tb"}
	if err := repo.Upsert(ctx, b); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, b.ID) })
	if b.IsDefault {
		t.Fatalf("the second connection must not steal the default")
	}

	if err := repo.SetDefault(ctx, b.ID); err != nil {
		t.Fatalf("set default b: %v", err)
	}
	assertSingleDefault(t, repo, b.ID)

	a.IsDefault = true
	if err := repo.Upsert(ctx, a); err != nil {
		t.Fatalf("save a as default: %v", err)
	}
	assertSingleDefault(t, repo, a.ID)

	// An empty token on update keeps the stored secret.
	a.Token = ""
	if err := repo.Upsert(ctx, a); err != nil {
		t.Fatalf("re-upsert a with a blank token: %v", err)
	}
	if got, err := repo.GetByID(ctx, a.ID); err != nil {
		t.Fatalf("get a: %v", err)
	} else if got.Token != "ta" {
		t.Fatalf("stored token clobbered: %q", got.Token)
	}

	// Deleting the default promotes the remaining connection.
	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatalf("delete a: %v", err)
	}
	assertSingleDefault(t, repo, b.ID)

	if err := repo.Delete(ctx, b.ID); err != nil {
		t.Fatalf("delete b: %v", err)
	}
	if _, err := repo.Default(ctx); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("with no connections, Default should be ErrNotFound, got %v", err)
	}
}

func assertSingleDefault(t *testing.T, repo repository.ConnectRepository, wantID string) {
	t.Helper()
	items, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defaults := 0
	for _, it := range items {
		if it.IsDefault {
			defaults++
			if it.ID != wantID {
				t.Fatalf("default is %s, want %s", it.ID, wantID)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default, got %d", defaults)
	}
	def, err := repo.Default(context.Background())
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if def.ID != wantID {
		t.Fatalf("Default() returned %s, want %s", def.ID, wantID)
	}
}

// Re-seeing a chat refreshes its row rather than adding one, and an update that
// arrives out of order must not walk the last-seen timestamp backwards.
func TestTelegramRecipientUpsert(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := st.TelegramRecipients()

	const connection, alias = "cnx_test", "bot_test"
	rec := &models.TelegramRecipient{Connection: connection, Alias: alias, ChatID: "100",
		Title: "Ops", LastSeenAt: 500}
	if err := repo.Upsert(ctx, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, rec.ID) })

	// An older sighting of the same chat: same row, timestamp unchanged.
	older := &models.TelegramRecipient{Connection: connection, Alias: alias, ChatID: "100",
		Title: "Ops renamed", LastSeenAt: 100}
	if err := repo.Upsert(ctx, older); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	items, err := repo.List(ctx, connection, alias, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("re-seeing a chat created %d rows, want 1", len(items))
	}
	if items[0].LastSeenAt != 500 {
		t.Fatalf("last seen walked backwards to %d", items[0].LastSeenAt)
	}
	if items[0].Title != "Ops renamed" {
		t.Fatalf("profile fields not refreshed: %q", items[0].Title)
	}

	// Another bot's chats are a different scope.
	if items, err := repo.List(ctx, connection, "other_bot", 10); err != nil {
		t.Fatalf("list other scope: %v", err)
	} else if len(items) != 0 {
		t.Fatalf("recipients leaked across bot scopes: %d rows", len(items))
	}
}

// Document stores live in postgres: the table is provisioned on create, the
// documents are JSONB, and a read query runs in a read-only transaction.
func TestDocumentStore(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	mem := st.Memory()

	store := &models.MemoryStore{
		Name: "docs", Type: models.MemoryDocument,
		Document: &models.DocumentMemoryConfig{Table: "test_docs_pg"},
	}
	if err := mem.Create(ctx, store); err != nil {
		t.Fatalf("create document store: %v", err)
	}
	t.Cleanup(func() { _ = mem.Delete(ctx, store.ID) })

	id, err := mem.WriteDocument(ctx, store, map[string]any{"customer": "acme", "amount": 42})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := mem.UpdateDocument(ctx, store, id, map[string]any{"customer": "acme", "amount": 43}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := mem.UpdateDocument(ctx, store, "doc_missing", map[string]any{}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("updating a missing document should be ErrNotFound, got %v", err)
	}

	// A read is the query author's SQL in the database's own dialect — here the
	// postgres JSON operators.
	query, err := models.ValidateReadSQL(`SELECT data->>'customer' AS customer FROM test_docs_pg`)
	if err != nil {
		t.Fatalf("validate read: %v", err)
	}
	rows, err := mem.RunReadQuery(ctx, store, query)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 || rows[0]["customer"] != "acme" {
		t.Fatalf("read returned %+v", rows)
	}

	if err := mem.DeleteDocument(ctx, store, id); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	if err := mem.DeleteDocument(ctx, store, id); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleting twice should be ErrNotFound, got %v", err)
	}
}

// Vector memory on postgres: the store row is in postgres, the index is in the
// sqlite sidecar, and search behaves exactly as on sqlite (partition scoping,
// metadata filter, score threshold).
func TestVectorStoreUsesSqliteSidecar(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	mem := st.Memory()

	store := &models.MemoryStore{
		Name: "vectors", Type: models.MemoryVector,
		Vector: &models.VectorMemoryConfig{Dimensions: 3, Metric: models.MetricCosine},
	}
	if err := mem.Create(ctx, store); err != nil {
		t.Fatalf("create vector store: %v", err)
	}
	t.Cleanup(func() { _ = mem.Delete(ctx, store.ID) })

	// The row itself is postgres's.
	if got, err := mem.GetByID(ctx, store.ID); err != nil {
		t.Fatalf("get store: %v", err)
	} else if got.Vector == nil || got.Vector.Dimensions != 3 {
		t.Fatalf("vector config lost: %+v", got.Vector)
	}

	index := func(partition, content string, v []float32) string {
		id, err := mem.IndexVector(ctx, store, content, v, map[string]any{"tag": content}, partition)
		if err != nil {
			t.Fatalf("index %s/%s: %v", partition, content, err)
		}
		return id
	}
	idA := index("p1", "a", []float32{1, 0, 0})
	index("p1", "b", []float32{0.9, 0.1, 0})
	index("p2", "c", []float32{1, 0, 0})

	query := []float32{1, 0, 0}
	p1, err := mem.SearchVectors(ctx, store, query, 10, "p1", 0, nil)
	if err != nil {
		t.Fatalf("search p1: %v", err)
	}
	if len(p1) != 2 {
		t.Fatalf("partition-scoped search returned %d hits, want 2", len(p1))
	}
	if p1[0].Content != "a" || p1[0].Partition != "p1" || p1[0].Score <= p1[1].Score {
		t.Fatalf("hits are not nearest-first with their partition: %+v", p1)
	}

	filtered, err := mem.SearchVectors(ctx, store, query, 10, "", 0, map[string]any{"tag": "c"})
	if err != nil {
		t.Fatalf("filtered search: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Content != "c" {
		t.Fatalf("metadata filter returned %+v", filtered)
	}

	// A mismatched embedding width is refused before it reaches the index.
	if _, err := mem.IndexVector(ctx, store, "wrong", []float32{1, 0}, nil, ""); err == nil {
		t.Fatalf("indexing a 2-d vector into a 3-d store should fail")
	}

	if err := mem.DeleteVector(ctx, store, idA); err != nil {
		t.Fatalf("delete vector: %v", err)
	}
	if err := mem.DeleteVector(ctx, store, idA); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleting twice should be ErrNotFound, got %v", err)
	}
}

// A vector store cannot be created when no sidecar is reachable: a store the app
// can neither index nor search must not exist at all.
func TestVectorStoreRefusedWithoutSidecar(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the postgres repository tests")
	}
	SetVectorSource(VectorDisabled)
	t.Cleanup(func() { SetVectorSource("") })

	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	store := &models.MemoryStore{Name: "vectors", Type: models.MemoryVector,
		Vector: &models.VectorMemoryConfig{Dimensions: 3}}
	if err := st.Memory().Create(context.Background(), store); err == nil {
		t.Fatalf("creating a vector store without an index should fail")
	}
	if store.ID != "" {
		if _, err := st.Memory().GetByID(context.Background(), store.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("a refused vector store left a row behind: %v", err)
		}
	}
}

// A probe must answer "can I use this?" without creating anything, and must
// report an unreachable server as an error rather than a panic.
func TestProbe(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the postgres repository tests")
	}
	res, err := Probe(context.Background(), dsn)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !res.Writable {
		t.Fatalf("the test database should be writable: %+v", res)
	}
	if res.Server == "" {
		t.Fatalf("probe did not report the server version: %+v", res)
	}

	if _, err := Probe(context.Background(),
		"postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=2"); err == nil {
		t.Fatalf("probing an unreachable server should fail")
	}
}

// DB_LOCAL_TABLES is parsed strictly: the shorthands expand, and a typo is an
// error rather than a table silently sent to the wrong database.
func TestParseLocalEntities(t *testing.T) {
	got, err := parseLocalEntities([]string{"config"})
	if err != nil {
		t.Fatalf("config shorthand: %v", err)
	}
	if !got[EntityNodeSettings] || !got[EntityConnect] || len(got) != 2 {
		t.Fatalf(`"config" expanded to %v`, got)
	}
	if got, err := parseLocalEntities([]string{"all"}); err != nil || len(got) != len(LocalEntityKeys) {
		t.Fatalf(`"all" expanded to %v (err %v)`, got, err)
	}
	if got, err := parseLocalEntities([]string{"connect", "none"}); err != nil || len(got) != 0 {
		t.Fatalf(`"none" should clear the set, got %v (err %v)`, got, err)
	}
	if _, err := parseLocalEntities([]string{"conect"}); err == nil {
		t.Fatalf("a misspelled entity should be rejected")
	}
}

// The claim the settings panel makes when you switch to postgres — "vector
// indexes do not move; stores created after the switch index into that same
// file, and stores created before it reappear if you switch back" — asserted
// end to end against one sqlite file shared by both backends.
func TestVectorIndexSurvivesASwitch(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the postgres repository tests")
	}
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "flomorphic.db")
	query := []float32{1, 0, 0}
	newStore := func(name string) *models.MemoryStore {
		return &models.MemoryStore{
			Name: name, Type: models.MemoryVector,
			Vector: &models.VectorMemoryConfig{Dimensions: 3, Metric: models.MetricCosine},
		}
	}

	// --- before the switch: a vector store on sqlite ---
	sq, err := repository.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	before := newStore("before the switch")
	if err := sq.Memory().Create(ctx, before); err != nil {
		t.Fatalf("create on sqlite: %v", err)
	}
	if _, err := sq.Memory().IndexVector(ctx, before, "sqlite-era", query, nil, ""); err != nil {
		t.Fatalf("index on sqlite: %v", err)
	}
	if err := sq.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	// --- the switch: postgres, with its vector sidecar on that same file ---
	SetVectorSource(file)
	t.Cleanup(func() { SetVectorSource("") })
	pg, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}

	// The store's ROW lived in sqlite, so it is not in this database — that is
	// what "no data is copied" means, and the panel says so.
	if _, err := pg.Memory().GetByID(ctx, before.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("the sqlite store should not appear on postgres: %v", err)
	}

	// A store created now keeps its row in postgres and its index in the file.
	after := newStore("after the switch")
	if err := pg.Memory().Create(ctx, after); err != nil {
		t.Fatalf("create on postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Memory().Delete(ctx, after.ID) })
	if _, err := pg.Memory().IndexVector(ctx, after, "postgres-era", query, nil, ""); err != nil {
		t.Fatalf("index on postgres: %v", err)
	}
	hits, err := pg.Memory().SearchVectors(ctx, after, query, 5, "", 0, nil)
	if err != nil {
		t.Fatalf("search on postgres: %v", err)
	}
	if len(hits) != 1 || hits[0].Content != "postgres-era" {
		t.Fatalf("search on postgres returned %+v", hits)
	}
	// The index really is in the sqlite file, not in postgres.
	if _, err := pg.Memory().RunReadQuery(ctx, after, "SELECT 1"); err == nil {
		t.Fatalf("a vector store must not answer document reads")
	}
	if err := pg.Close(); err != nil {
		t.Fatalf("close postgres: %v", err)
	}

	// --- switching back: the original store is there, and still searchable ---
	sq2, err := repository.Open("sqlite", file)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	defer sq2.Close()
	back, err := sq2.Memory().GetByID(ctx, before.ID)
	if err != nil {
		t.Fatalf("the pre-switch store did not come back: %v", err)
	}
	hits, err = sq2.Memory().SearchVectors(ctx, back, query, 5, "", 0, nil)
	if err != nil {
		t.Fatalf("search after switching back: %v", err)
	}
	if len(hits) != 1 || hits[0].Content != "sqlite-era" {
		t.Fatalf("the pre-switch index did not survive: %+v", hits)
	}
	// And the index the postgres era wrote is in this same file, so switching
	// forward again finds it rather than an empty one.
	if hits, err := sq2.Memory().SearchVectors(ctx, after, query, 5, "", 0, nil); err != nil {
		t.Fatalf("search the postgres-era index from sqlite: %v", err)
	} else if len(hits) != 1 || hits[0].Content != "postgres-era" {
		t.Fatalf("the postgres-era index is not in the shared file: %+v", hits)
	}
}

// The flow list is the summary — no graph read, no graph sent — with the node
// count derived from the stored JSON. The count is why this needs a server: the
// guarded jsonb_array_length subquery (see workflowSummaryColumns) is the one
// expression here sqlite's twin cannot vouch for, and it has to hold for a flow
// with no graph and for the blank text an un-migrated row carries.
func TestWorkflowListSummary(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	flows := st.Workflows()

	graphed := &models.FlowRecord{Title: "pg summary graphed", ViewFlow: compiler.VueFlow{
		Nodes: []compiler.VueFlowNode{{ID: "n1"}, {ID: "n2"}},
		Edges: []compiler.Edges{{ID: "e1", Source: "n1", Target: "n2"}},
	}}
	if err := flows.Upsert(ctx, graphed); err != nil {
		t.Fatalf("upsert graphed: %v", err)
	}
	t.Cleanup(func() { _ = flows.Delete(ctx, graphed.ID) })
	legacy := &models.FlowRecord{Title: "pg summary legacy"}
	if err := flows.Upsert(ctx, legacy); err != nil {
		t.Fatalf("upsert legacy: %v", err)
	}
	t.Cleanup(func() { _ = flows.Delete(ctx, legacy.ID) })

	db, err := sql.Open("pgx", os.Getenv("TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE workflows SET view_flow = '' WHERE id = $1`, legacy.ID); err != nil {
		t.Fatalf("blank view_flow: %v", err)
	}

	items, _, err := flows.List(ctx, repository.ListParams{Search: "pg summary", Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Title] = it.NodeCount
	}
	if len(counts) != 2 {
		t.Fatalf("list returned %d rows, want 2: %+v", len(items), items)
	}
	if counts["pg summary graphed"] != 2 {
		t.Fatalf("node count = %d, want 2", counts["pg summary graphed"])
	}
	if counts["pg summary legacy"] != 0 {
		t.Fatalf("blank view_flow counted %d nodes, want 0", counts["pg summary legacy"])
	}

	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal summaries: %v", err)
	}
	if strings.Contains(string(payload), "view_flow") {
		t.Fatalf("list payload carries the graph: %s", payload)
	}

	// ListRecords is the same page read whole — one query with the graphs, for
	// the MCP tool. Same rows and order as the summaries.
	recs, _, err := flows.ListRecords(ctx, repository.ListParams{Search: "pg summary", Limit: 10})
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(recs) != len(items) {
		t.Fatalf("records page = %d rows, summaries = %d", len(recs), len(items))
	}
	for i, rec := range recs {
		if rec.ID != items[i].ID {
			t.Fatalf("row %d: records %s, summaries %s — the pages disagree", i, rec.ID, items[i].ID)
		}
		if len(rec.ViewFlow.Nodes) != items[i].NodeCount {
			t.Fatalf("row %d (%s): %d nodes in the record, nodeCount %d", i, rec.ID, len(rec.ViewFlow.Nodes), items[i].NodeCount)
		}
	}
}

// The derived plugin list, on a real server: the column round-trips through the
// hand-written upsert and both selects, the save computes the list from the
// graph instead of trusting the caller, and a list row carries it.
func TestWorkflowPlugins(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	flows := st.Workflows()

	rec := &models.FlowRecord{
		Title:   "pg plugins",
		Plugins: []models.FlowPlugin{{Kind: models.KindExtension, PluginID: "not-in-this-graph"}},
		ViewFlow: compiler.VueFlow{
			Nodes: []compiler.VueFlowNode{
				{ID: "start", Type: "startNode"},
				{ID: "ask", Type: "llm", Data: map[string]any{"pluginId": "aaaa-bbbb-cccc-llm0"}},
				{ID: "task", Type: "plugin", Data: map[string]any{"pluginId": "jira-local", "action": "jira.add_task"}},
				{ID: "parked", Type: "http", Data: map[string]any{"pluginId": "aaaa-bbbb-cccc-http"}},
			},
			Edges: []compiler.Edges{
				{ID: "e1", Source: "start", Target: "ask"},
				{ID: "e2", Source: "ask", Target: "task"},
			},
		},
	}
	if err := flows.Upsert(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = flows.Delete(ctx, rec.ID) })

	want := []models.FlowPlugin{
		{Kind: models.KindBuiltin, Type: models.ExtLLMType, PluginID: "aaaa-bbbb-cccc-llm0", NodeCount: 1},
		{Kind: models.KindExtension, Type: models.ExtPluginBaseType, PluginID: "jira-local",
			Actions: []string{"jira.add_task"}, NodeCount: 1},
	}
	if !reflect.DeepEqual(rec.Plugins, want) {
		t.Fatalf("saved plugins = %+v, want %+v", rec.Plugins, want)
	}
	got, err := flows.GetByID(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got.Plugins, want) {
		t.Fatalf("stored plugins = %+v, want %+v", got.Plugins, want)
	}
	items, _, err := flows.List(ctx, repository.ListParams{Search: "pg plugins", Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || !reflect.DeepEqual(items[0].Plugins, want) {
		t.Fatalf("listed plugins = %+v, want %+v", items, want)
	}
}
