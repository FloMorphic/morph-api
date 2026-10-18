package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FloMorphic/morph-api/models"
)

// closeGrace is how long a replaced Store is left open after a switch. In-flight
// requests still hold its repositories (they resolved them before the swap), and
// closing the connection under them would fail those requests for no reason. A
// few seconds is far longer than any CRUD call and bounds the leak to one
// connection pool per switch.
const closeGrace = 10 * time.Second

// Switchable is a Store that can be repointed at a different database while the
// process runs — what the database panel's switch does.
//
// It is the Store every controller, scheduler and service handler is given, and
// the only one they ever hold: each accessor returns a stable forwarder that
// resolves the live backend per call. So a switch needs no re-wiring of the HTTP
// layer and no restart; the next query simply lands on the new database. The
// pieces that cache a row in memory (an armed scheduler timer) are nudged
// through the OnSwitch hooks.
type Switchable struct {
	mu      sync.Mutex // serializes switches
	current atomic.Pointer[Store]
	cfg     atomic.Pointer[Config]

	hooksMu sync.Mutex
	hooks   []func()

	workflows    *crudFwd[models.FlowRecord]
	contexts     *crudFwd[models.ContextRecord]
	nodeSettings *crudFwd[models.NodeSetting]
	prompts      *promptFwd
	humanTasks   *humanTaskFwd
	extensions   *extensionFwd
	triggers     *triggerFwd
	memory       *memoryFwd
	processes    *processFwd
	connect      *connectFwd
	tgRecipients *telegramFwd
}

// NewSwitchable wraps an already-open Store, remembering the config it was
// opened from so the panel can report what is live.
func NewSwitchable(cfg Config, st Store) *Switchable {
	s := &Switchable{}
	s.current.Store(&st)
	normalized := cfg.Normalized()
	s.cfg.Store(&normalized)

	s.workflows = &crudFwd[models.FlowRecord]{s: s, pick: func(st Store) CRUD[models.FlowRecord] { return st.Workflows() }}
	s.contexts = &crudFwd[models.ContextRecord]{s: s, pick: func(st Store) CRUD[models.ContextRecord] { return st.Contexts() }}
	s.nodeSettings = &crudFwd[models.NodeSetting]{s: s, pick: func(st Store) CRUD[models.NodeSetting] { return st.NodeSettings() }}
	s.prompts = &promptFwd{crudFwd[models.PromptRecord]{s: s, pick: func(st Store) CRUD[models.PromptRecord] { return st.Prompts() }}}
	s.humanTasks = &humanTaskFwd{crudFwd[models.HumanTask]{s: s, pick: func(st Store) CRUD[models.HumanTask] { return st.HumanTasks() }}}
	s.extensions = &extensionFwd{crudFwd[models.ExtensionRecord]{s: s, pick: func(st Store) CRUD[models.ExtensionRecord] { return st.Extensions() }}}
	s.triggers = &triggerFwd{crudFwd[models.Trigger]{s: s, pick: func(st Store) CRUD[models.Trigger] { return st.Triggers() }}}
	s.memory = &memoryFwd{s: s}
	s.processes = &processFwd{s: s}
	s.connect = &connectFwd{s: s}
	s.tgRecipients = &telegramFwd{s: s}
	return s
}

// live returns the Store queries currently go to.
func (s *Switchable) live() Store { return *s.current.Load() }

// Live exposes the backend in use, for the rare caller that needs the concrete
// store (a probe of the live database, a backend-specific capability check)
// rather than a forwarder.
func (s *Switchable) Live() Store { return s.live() }

// Config reports which database is live. The Source is NOT redacted — callers
// that hand it to a client must redact it themselves (see Config.Redacted).
func (s *Switchable) Config() Config { return *s.cfg.Load() }

// OnSwitch registers a callback to run after a successful switch. It is how the
// runtime's in-memory state is refreshed: the schedulers re-read their next due
// row from the new database instead of waiting out a timer armed against the old
// one. Callbacks run synchronously, in registration order, and must not block.
func (s *Switchable) OnSwitch(fn func()) {
	if fn == nil {
		return
	}
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	s.hooks = append(s.hooks, fn)
}

// Switch opens cfg and, only once that succeeded, makes it the live database.
// The old Store is closed after a grace period so requests already running on
// it finish normally. A failure to open leaves the current database untouched —
// a mistyped URI costs the operator an error message, never the service.
func (s *Switchable) Switch(cfg Config) error {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	next, err := Open(cfg.Driver, cfg.Source)
	if err != nil {
		return fmt.Errorf("switch to %s: %w", cfg.Driver, err)
	}
	if err := PingStore(context.Background(), next); err != nil {
		_ = next.Close()
		return fmt.Errorf("switch to %s: ping: %w", cfg.Driver, err)
	}

	prev := s.live()
	s.current.Store(&next)
	s.cfg.Store(&cfg)

	if prev != next {
		go func() {
			time.Sleep(closeGrace)
			if err := prev.Close(); err != nil {
				fmt.Printf("warning: closing previous database: %v\n", err)
			}
		}()
	}

	s.hooksMu.Lock()
	hooks := append([]func(){}, s.hooks...)
	s.hooksMu.Unlock()
	for _, fn := range hooks {
		fn()
	}
	return nil
}

// Ping checks the live database, so a Switchable is itself a Pinger.
func (s *Switchable) Ping(ctx context.Context) error { return PingStore(ctx, s.live()) }

// Close closes the live database. The facade stays usable only in the sense that
// calls will error — it is called on shutdown.
func (s *Switchable) Close() error { return s.live().Close() }

func (s *Switchable) Workflows() WorkflowRepository       { return s.workflows }
func (s *Switchable) Contexts() ContextRepository         { return s.contexts }
func (s *Switchable) NodeSettings() NodeSettingRepository { return s.nodeSettings }
func (s *Switchable) Prompts() PromptRepository           { return s.prompts }
func (s *Switchable) HumanTasks() HumanTaskRepository     { return s.humanTasks }
func (s *Switchable) Extensions() ExtensionRepository     { return s.extensions }
func (s *Switchable) Triggers() TriggerRepository         { return s.triggers }
func (s *Switchable) Memory() MemoryRepository            { return s.memory }
func (s *Switchable) Processes() ProcessRepository        { return s.processes }
func (s *Switchable) Connect() ConnectRepository          { return s.connect }
func (s *Switchable) TelegramRecipients() TelegramRecipientRepository {
	return s.tgRecipients
}

// crudFwd forwards the shared CRUD contract to whichever backend is live. `pick`
// selects the entity's repository from a Store, so one generic forwarder covers
// every CRUD entity.
type crudFwd[T any] struct {
	s    *Switchable
	pick func(Store) CRUD[T]
}

func (f *crudFwd[T]) repo() CRUD[T] { return f.pick(f.s.live()) }

func (f *crudFwd[T]) Upsert(ctx context.Context, v *T) error { return f.repo().Upsert(ctx, v) }
func (f *crudFwd[T]) GetByID(ctx context.Context, id string) (*T, error) {
	return f.repo().GetByID(ctx, id)
}
func (f *crudFwd[T]) List(ctx context.Context, p ListParams) ([]T, int64, error) {
	return f.repo().List(ctx, p)
}
func (f *crudFwd[T]) Delete(ctx context.Context, id string) error { return f.repo().Delete(ctx, id) }

// promptFwd adds the prompt repository's seeding method to the CRUD forwarder.
type promptFwd struct{ crudFwd[models.PromptRecord] }

func (f *promptFwd) SeedPrompts(ctx context.Context, defs []models.PromptRecord) (int, error) {
	return f.s.live().Prompts().SeedPrompts(ctx, defs)
}

// humanTaskFwd adds the HITL actions to the CRUD forwarder.
type humanTaskFwd struct{ crudFwd[models.HumanTask] }

func (f *humanTaskFwd) Answer(ctx context.Context, id, questionID, answer string) (*models.HumanTask, error) {
	return f.s.live().HumanTasks().Answer(ctx, id, questionID, answer)
}
func (f *humanTaskFwd) AppendMessage(ctx context.Context, id string, msg models.HumanTaskMessage) (*models.HumanTask, error) {
	return f.s.live().HumanTasks().AppendMessage(ctx, id, msg)
}
func (f *humanTaskFwd) Close(ctx context.Context, id string) (*models.HumanTask, error) {
	return f.s.live().HumanTasks().Close(ctx, id)
}

// extensionFwd adds the palette's seeding and plugin-action methods.
type extensionFwd struct {
	crudFwd[models.ExtensionRecord]
}

func (f *extensionFwd) SeedBuiltins(ctx context.Context, defs []models.ExtensionRecord) (int, error) {
	return f.s.live().Extensions().SeedBuiltins(ctx, defs)
}
func (f *extensionFwd) ListPluginActions(ctx context.Context, pluginID string) ([]models.ExtensionRecord, error) {
	return f.s.live().Extensions().ListPluginActions(ctx, pluginID)
}
func (f *extensionFwd) DeletePluginActions(ctx context.Context, pluginID string) (int, error) {
	return f.s.live().Extensions().DeletePluginActions(ctx, pluginID)
}

// triggerFwd adds the trigger runtime lookups and state writers.
type triggerFwd struct{ crudFwd[models.Trigger] }

func (f *triggerFwd) GetBySlug(ctx context.Context, slug string) (*models.Trigger, error) {
	return f.s.live().Triggers().GetBySlug(ctx, slug)
}
func (f *triggerFwd) ListEnabledSchedules(ctx context.Context) ([]models.Trigger, error) {
	return f.s.live().Triggers().ListEnabledSchedules(ctx)
}
func (f *triggerFwd) RecordHit(ctx context.Context, id string, hit models.TriggerHit) error {
	return f.s.live().Triggers().RecordHit(ctx, id, hit)
}
func (f *triggerFwd) MarkFired(ctx context.Context, id string, at int64) error {
	return f.s.live().Triggers().MarkFired(ctx, id, at)
}

// memoryFwd forwards the memory repository, whose contract is its own (no
// pagination, plus the document and vector operations).
type memoryFwd struct{ s *Switchable }

func (f *memoryFwd) repo() MemoryRepository { return f.s.live().Memory() }

func (f *memoryFwd) Create(ctx context.Context, m *models.MemoryStore) error {
	return f.repo().Create(ctx, m)
}
func (f *memoryFwd) GetByID(ctx context.Context, id string) (*models.MemoryStore, error) {
	return f.repo().GetByID(ctx, id)
}
func (f *memoryFwd) List(ctx context.Context) ([]models.MemoryStore, error) {
	return f.repo().List(ctx)
}
func (f *memoryFwd) Delete(ctx context.Context, id string) error { return f.repo().Delete(ctx, id) }
func (f *memoryFwd) RunReadQuery(ctx context.Context, store *models.MemoryStore, query string) ([]map[string]any, error) {
	return f.repo().RunReadQuery(ctx, store, query)
}
func (f *memoryFwd) WriteDocument(ctx context.Context, store *models.MemoryStore, doc map[string]any) (string, error) {
	return f.repo().WriteDocument(ctx, store, doc)
}
func (f *memoryFwd) UpdateDocument(ctx context.Context, store *models.MemoryStore, id string, doc map[string]any) error {
	return f.repo().UpdateDocument(ctx, store, id, doc)
}
func (f *memoryFwd) DeleteDocument(ctx context.Context, store *models.MemoryStore, id string) error {
	return f.repo().DeleteDocument(ctx, store, id)
}
func (f *memoryFwd) IndexVector(ctx context.Context, store *models.MemoryStore, content string, vector []float32, metadata map[string]any, partition string) (string, error) {
	return f.repo().IndexVector(ctx, store, content, vector, metadata, partition)
}
func (f *memoryFwd) SearchVectors(ctx context.Context, store *models.MemoryStore, vector []float32, k int, partition string, minScore float64, filter map[string]any) ([]models.VectorMatch, error) {
	return f.repo().SearchVectors(ctx, store, vector, k, partition, minScore, filter)
}
func (f *memoryFwd) DeleteVector(ctx context.Context, store *models.MemoryStore, docID string) error {
	return f.repo().DeleteVector(ctx, store, docID)
}

// processFwd forwards the process repository (keyed by an int64 index, with
// Create/Update split and the scheduler's lookups).
type processFwd struct{ s *Switchable }

func (f *processFwd) repo() ProcessRepository { return f.s.live().Processes() }

func (f *processFwd) Create(ctx context.Context, p *models.Process) error {
	return f.repo().Create(ctx, p)
}
func (f *processFwd) Update(ctx context.Context, p *models.Process) error {
	return f.repo().Update(ctx, p)
}
func (f *processFwd) GetByIndex(ctx context.Context, indexID int64) (*models.Process, error) {
	return f.repo().GetByIndex(ctx, indexID)
}
func (f *processFwd) List(ctx context.Context, p ListParams) ([]models.Process, int64, error) {
	return f.repo().List(ctx, p)
}
func (f *processFwd) Delete(ctx context.Context, indexID int64) error {
	return f.repo().Delete(ctx, indexID)
}
func (f *processFwd) GetRunningByPID(ctx context.Context, pid string) (*models.Process, error) {
	return f.repo().GetRunningByPID(ctx, pid)
}
func (f *processFwd) NextScheduled(ctx context.Context) (*models.Process, error) {
	return f.repo().NextScheduled(ctx)
}
func (f *processFwd) ListDueScheduled(ctx context.Context, now int64) ([]models.Process, error) {
	return f.repo().ListDueScheduled(ctx, now)
}

// connectFwd forwards the OpenConnector gateway repository.
type connectFwd struct{ s *Switchable }

func (f *connectFwd) repo() ConnectRepository { return f.s.live().Connect() }

func (f *connectFwd) Upsert(ctx context.Context, c *models.ConnectConnection) error {
	return f.repo().Upsert(ctx, c)
}
func (f *connectFwd) GetByID(ctx context.Context, id string) (*models.ConnectConnection, error) {
	return f.repo().GetByID(ctx, id)
}
func (f *connectFwd) List(ctx context.Context) ([]models.ConnectConnection, error) {
	return f.repo().List(ctx)
}
func (f *connectFwd) Default(ctx context.Context) (*models.ConnectConnection, error) {
	return f.repo().Default(ctx)
}
func (f *connectFwd) Delete(ctx context.Context, id string) error { return f.repo().Delete(ctx, id) }
func (f *connectFwd) SetDefault(ctx context.Context, id string) error {
	return f.repo().SetDefault(ctx, id)
}

// telegramFwd forwards the Telegram recipient directory.
type telegramFwd struct{ s *Switchable }

func (f *telegramFwd) repo() TelegramRecipientRepository { return f.s.live().TelegramRecipients() }

func (f *telegramFwd) Upsert(ctx context.Context, rec *models.TelegramRecipient) error {
	return f.repo().Upsert(ctx, rec)
}
func (f *telegramFwd) List(ctx context.Context, connection, alias string, limit int) ([]models.TelegramRecipient, error) {
	return f.repo().List(ctx, connection, alias, limit)
}
func (f *telegramFwd) Delete(ctx context.Context, id string) error { return f.repo().Delete(ctx, id) }
