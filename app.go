package main
import (
	"context"
	"fmt"

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
	// infra API is configured, and treat connection failures as non-fatal so
	// the CRUD API keeps serving.
	if env.GetInfraApiUrl() == "" {
		fmt.Println("inflow runtime disabled (INFLOW_INFRA_API not set) — serving CRUD only")
	} else if err := inflow.InitInflowConnection(store); err != nil {
		fmt.Printf("warning: inflow runtime not connected: %v\n", err)
	} else if err := inflow.LoadSvcNodehandlers(store); err != nil {
		fmt.Printf("warning: inflow service handlers not loaded: %v\n", err)
	} else {
		// Scheduler: launches `scheduled` process rows (Continue After resumes)
		// when their time arrives. Timer-armed on the nearest row, woken when a
		// new one is recorded; its first pass catches rows that came due while
		// the app was down. Needs the runtime, so it lives in this branch.
		inflow.StartScheduler(context.Background(), store)
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
