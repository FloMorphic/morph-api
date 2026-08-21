package main
import (
	"context"
	"fmt"
	"time"

	apiHandlers "github.com/FloMorphic/morph-api/api"
	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/inflow"
	"github.com/FloMorphic/morph-api/repository"
	// Register the sqlite repository driver (side-effecting init).
	_ "github.com/FloMorphic/morph-api/repository/sqlite"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/logger"
)

func main() {
	// Persistence — driver selected by env (sqlite by default).
	store, err := repository.Open(env.GetDBDriver(), env.GetDBSource())
	if err != nil {
		panic(err)
	}
	defer store.Close()

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
}
