package main
import (
	"context"
	"fmt"
	"time"

	apiHandlers "github.com/FloMorphic/morph-api/api"
	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/repository"
	// Register the repository drivers (side-effecting init). sqlite is the
	// default and always linked (it also hosts the vector indexes); postgres is
	// the optional backend an operator can select from the admin panel.
	_ "github.com/FloMorphic/morph-api/repository/postgres"
	_ "github.com/FloMorphic/morph-api/repository/sqlite"

	fuse "github.com/Inflowenger/inflow-fusion/inflow"
	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/logger"
)

func main() {
	// Persistence — sqlite by default, or whatever the admin panel last selected
	// (see resolveDBConfig). The store is wrapped in a Switchable so the panel
	// can repoint it at another database without a restart: every controller,
	// scheduler and service handler below is handed that one facade and resolves
	// the live backend per call.
	cfg := resolveDBConfig()
	opened, err := repository.Open(cfg.Driver, cfg.Source)
	if err != nil {
		panic(err)
	}
	store := repository.NewSwitchable(cfg, opened)
	defer store.Close()

	// A switch invalidates both schedulers' armed timers — they were computed
	// from the previous database's nearest due row — so wake them to re-read.
	store.OnSwitch(func() {
		inflow.NotifyScheduler()
		inflow.NotifyTriggerScheduler()
	})

	// Inflow runtime is optional in standalone/dev. Skip it entirely when no
	// infra API is configured; otherwise connect in the background with retry so
	// the CRUD API serves immediately and a late Infra (a host reboot brings the
	// containers up together, before inflow-infra is healthy) is picked up on its
	// own instead of leaving the API stuck CRUD-only until it is restarted.
	if env.GetInfraApiUrl() == "" {
		fmt.Println("inflow runtime disabled (INFLOW_INFRA_API not set) — serving CRUD only")
	} else {
		go connectInflowWithRetry(context.Background(), store)
	}

	app := fiber.New(fiber.Config{
		JSONEncoder: sonic.Marshal,
		JSONDecoder: sonic.Unmarshal,
		AppName:     "flomorphic-api",
	})
	app.Use(cors.New())
	app.Use(logger.New())

	apiHandlers.RegisterAll(app, store)

	if err := app.Listen(env.GetApiPort()); err != nil {
		panic(err)
	}
}

// resolveDBConfig picks the database to open: the selection saved by the admin
// panel if there is one, else DB_DRIVER/DB_SOURCE (sqlite by default).
//
// The saved file wins over the environment deliberately. The environment is the
// deployment's default — what a fresh install or a reset falls back to — while
// the file is a decision an operator made on this instance, and it would be
// surprising for a restart to undo it. A file that exists but cannot be parsed
// is fatal: coming up on a different database than the one selected is worse
// than refusing to start.
func resolveDBConfig() repository.Config {
	envCfg := repository.Config{Driver: env.GetDBDriver(), Source: env.GetDBSource()}.Normalized()
	path := env.GetDBConfigFile()
	saved, err := repository.LoadConfig(path)
	if err != nil {
		panic(err)
	}
	if saved == nil {
		return envCfg
	}
	cfg := saved.Normalized()
	if err := cfg.Validate(); err != nil {
		fmt.Printf("warning: saved database selection in %s is unusable (%v) — falling back to the environment\n", path, err)
		return envCfg
	}
	fmt.Printf("database selection from %s: %s (%s)\n", path, cfg.Driver, repository.RedactSource(cfg.Source))
	return cfg
}

// connectInflowWithRetry connects to the inflow runtime and, once connected,
// wires the service handlers and schedulers. It keeps retrying the connection
// with a capped backoff because the failure it is built for is transient: on a
// host reboot Docker brings flomorphic up alongside inflow-infra, and the first
// connect attempts hit an Infra that is not answering yet. Without this the
// backend stays nil for the process's lifetime — every credential mint (adding
// an extension, running an LLM/MCP node) then answers 500 "backend initial is
// required before any request" until the container is manually restarted.
//
// InitInflowConnection is safe to call repeatedly: it fails before opening any
// NATS socket when Infra's REST API is unreachable, and only publishes the
// backend instance on full success, so a failed attempt leaves no half-open
// state for the next one.
func connectInflowWithRetry(ctx context.Context, store repository.Store) {
	const (
		firstDelay = 2 * time.Second
		maxDelay   = 30 * time.Second
	)
	delay := firstDelay
	for attempt := 1; ; attempt++ {
		if err := inflow.InitInflowConnection(store); err != nil {
			fmt.Printf("warning: inflow runtime not connected (attempt %d): %v — retrying in %s\n", attempt, err, delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < maxDelay {
				if delay *= 2; delay > maxDelay {
					delay = maxDelay
				}
			}
			continue
		}
		break
	}
	fmt.Println("inflow runtime connected")

	// The SDK's startup reload probed infra's engine list exactly once, just now.
	// On that same host reboot the fractal is usually still registering (or
	// crash-looping on a NATS that is not up yet), so the probe finds nothing and
	// the pool stays empty for the process's lifetime — "no resource" on every
	// run until the container is restarted or the engine is added by hand. Keep
	// looking in the background until something reachable is registered.
	go reloadResourcesWithRetry(ctx)

	if err := inflow.LoadSvcNodehandlers(store); err != nil {
		fmt.Printf("warning: inflow service handlers not loaded: %v\n", err)
		return
	}

	// OpenConnector services: let FloMorphic-coupled plugins resolve a connected
	// account and run actions through the central Connect credential over NATS.
	// Non-fatal — without it those plugins just can't reach the credential.
	if err := inflow.StartOCServices(store); err != nil {
		fmt.Printf("warning: OC services not started: %v\n", err)
	}

	// Scheduler: launches `scheduled` process rows (Continue After resumes) when
	// their time arrives. Timer-armed on the nearest row, woken when a new one is
	// recorded; its first pass catches rows that came due while the app was down.
	inflow.StartScheduler(ctx, store)

	// Trigger scheduler: arms recurring schedule triggers (cron/interval) and
	// starts a fresh run each time one comes due. Distinct from the process
	// scheduler above (which resumes one-shot parked runs).
	inflow.StartTriggerScheduler(ctx, store)

	// HITL Telegram bridge: holds a Human-in-the-Loop session in a Telegram chat
	// instead of the app — it opens the conversation, polls the bot's updates
	// through OpenConnector, and closes the task (releasing a parked flow) when the
	// person is done. Idle and silent unless a flow parks on a Telegram HITL node.
	inflow.StartHitlTelegramBridge(ctx, store)
}

// reloadResourcesWithRetry re-reads infra's engine list, with the same capped
// backoff as the connection above, until the dispatch pool holds at least one
// reachable resource. Each tick goes through ReloadResourcesIfEmpty, which
// fills the pool only if it is still empty: a resource an operator adds by
// hand while a tick is in flight is kept, and ends the loop. On a platform with
// no engine at all this is one cheap GET every maxDelay — the price of picking
// the engine up whenever it does appear, without anyone restarting the API.
func reloadResourcesWithRetry(ctx context.Context) {
	const (
		firstDelay = 2 * time.Second
		maxDelay   = 30 * time.Second
	)
	delay := firstDelay
	for attempt := 1; ; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		backend := fuse.GetInflowBackend()
		if backend == nil {
			return
		}
		filled, reachable, registered, err := backend.ReloadResourcesIfEmpty(100)
		switch {
		case err != nil:
			fmt.Printf("warning: inflow resource pool still empty (attempt %d): %v — retrying in %s\n", attempt, err, delay)
		case filled:
			fmt.Printf("inflow resource pool loaded on attempt %d: %d of %d registered resource(s) reachable\n", attempt, reachable, registered)
			return
		case fuse.HasLiveResources():
			// Filled by someone else meanwhile (a hand-added resource, an explicit
			// reload) — nothing left to wait for.
			return
		default:
			fmt.Printf("warning: inflow resource pool still empty (attempt %d): none of %d registered resource(s) reachable — retrying in %s\n", attempt, registered, delay)
		}
		if delay < maxDelay {
			if delay *= 2; delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}
