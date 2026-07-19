package env

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// getEnvVar reads a variable from the process env, falling back to a .env file
// (optionally .env.<ENV>). Mirrors the inspector-api loader.
func getEnvVar(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	envFile := ".env"
	if appEnv := os.Getenv("ENV"); appEnv != "" {
		envFile = envFile + "." + appEnv
	}
	_ = godotenv.Load(envFile)

	v, ok := os.LookupEnv(key)
	if !ok {
		fmt.Println(fmt.Errorf("environment variable not set: %s", key))
	}
	return v
}

// GetApiPort returns the listen address, e.g. ":8025". Accepts either "8025" or
// ":8025" in PORT.
func GetApiPort() string {
	if p := getEnvVar("PORT"); p != "" {
		if !strings.Contains(p, ":") {
			p = fmt.Sprintf(":%s", p)
		}
		return p
	}
	return ":8025"
}

// GetDBDriver selects the repository backend. Defaults to "sqlite". A future
// "postgres" driver can register itself and be selected here without touching
// the API or controllers.
func GetDBDriver() string {
	if d := getEnvVar("DB_DRIVER"); d != "" {
		return d
	}
	return "sqlite"
}

// GetDBSource is the driver-specific data source. For sqlite it is a file path
// (or DSN); defaults to "db/flomorphic.db". The sqlite driver creates the parent
// directory if it is missing.
func GetDBSource() string {
	if s := getEnvVar("DB_SOURCE"); s != "" {
		return s
	}
	return "db/flomorphic.db"
}

// GetInfraApiUrl is the inflowenger infra API base (INFLOW_INFRA_API).
func GetInfraApiUrl() string {
	return getEnvVar("INFLOW_INFRA_API")
}

// GetInfraJWTSecret is the shared secret used to authenticate with the infra API
// and to verify inbound API tokens (INFLOW_INFRA_JWT_SECRET).
func GetInfraJWTSecret() string {
	return getEnvVar("INFLOW_INFRA_JWT_SECRET")
}

// GetJwtSecret is the HS256 secret guarding this API's routes. Falls back to the
// infra secret so a single key can drive both, as in inspector-api.
func GetJwtSecret() string {
	if s := getEnvVar("API_JWT_SECRET"); s != "" {
		return s
	}
	return GetInfraJWTSecret()
}

// AuthEnabled toggles the JWT middleware on the CRUD route groups. Off by
// default so the web app works standalone; set AUTH_ENABLED=true to require a
// bearer token.
func AuthEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(getEnvVar("AUTH_ENABLED")))
	return v == "true" || v == "1" || v == "yes"
}
