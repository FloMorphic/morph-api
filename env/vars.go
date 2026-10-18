package env

import (
	"fmt"
	"os"
	"path/filepath"
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

// getEnvVarQuiet is getEnvVar without the "not set" notice. The notice is a
// useful boot-time hint for a variable a deployment is expected to set, but the
// database getters below are also read per request (the admin panel's state
// endpoint), where an unset optional variable is the normal case and the notice
// is just noise in the log.
func getEnvVarQuiet(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	envFile := ".env"
	if appEnv := os.Getenv("ENV"); appEnv != "" {
		envFile = envFile + "." + appEnv
	}
	_ = godotenv.Load(envFile)
	return os.Getenv(key)
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
	if d := getEnvVarQuiet("DB_DRIVER"); d != "" {
		return d
	}
	return "sqlite"
}

// GetDBSource is the driver-specific data source. For sqlite it is a file path
// (or DSN); defaults to "db/flomorphic.db". The sqlite driver creates the parent
// directory if it is missing.
func GetDBSource() string {
	if s := getEnvVarQuiet("DB_SOURCE"); s != "" {
		return s
	}
	return "db/flomorphic.db"
}

// GetInfraApiUrl is the inflowenger infra API base (INFLOW_INFRA_API).
func GetInfraApiUrl() string {
	return getEnvVar("INFLOW_INFRA_API")
}

// GetInfraNatsUrl is the NATS endpoint handed to plugins in the generated
// `INFRA_URL` (PLUGIN_INFRA_URL). It exists because the address this API reaches
// Infra on is not always the address a plugin can: the API usually runs inside
// the compose network ("infra:4222") while a user runs their plugin on a laptop
// or another host, which needs the published one. Empty means "use whatever the
// connected runtime reports" — see the extension controller's infraNatsURL.
func GetInfraNatsUrl() string {
	return strings.TrimSpace(getEnvVar("PLUGIN_INFRA_URL"))
}

// GetPublicApiUrl is the base URL callers reach this API on (PUBLIC_API_URL),
// used to build the plugin installer one-liner. Empty means "derive it from the
// request", which is right unless a proxy rewrites the Host header.
func GetPublicApiUrl() string {
	return strings.TrimRight(strings.TrimSpace(getEnvVar("PUBLIC_API_URL")), "/")
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

// MCPEnabled toggles the embedded MCP server mounted at /mcp (see mcpserver).
// On by default so an AI client can drive the API out of the box; set
// MCP_ENABLED=false (or 0/no) to leave the endpoint unmounted. When AuthEnabled
// is also on, /mcp sits behind the same bearer gate as the CRUD groups.
func MCPEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(getEnvVar("MCP_ENABLED")))
	return v == "" || v == "true" || v == "1" || v == "yes"
}

// GetDBConfigFile is where the database selection made in the admin panel is
// persisted (DB_CONFIG_FILE). It is read at boot and takes precedence over
// DB_DRIVER/DB_SOURCE, so a switch survives a restart without anyone editing
// the environment; deleting the file hands control back to the environment.
//
// The default puts it beside the sqlite database, which is the one directory a
// deployment is already guaranteed to have made writable (/data in the shipped
// container).
func GetDBConfigFile() string {
	if p := strings.TrimSpace(getEnvVarQuiet("DB_CONFIG_FILE")); p != "" {
		return p
	}
	dir := filepath.Dir(sqliteSourceDefault())
	if dir == "" || dir == "." {
		return "database.json"
	}
	return filepath.Join(dir, "database.json")
}

// GetVectorDBSource is the sqlite file that holds the vector indexes
// (DB_VECTOR_SOURCE). Vector memory is always sqlite-vec, including on a
// postgres deployment — see repository/postgres — so this is the one path that
// stays sqlite whatever DB_DRIVER says. Defaults to the deployment's sqlite
// file, so vector stores created before a switch to postgres are still indexed
// and searchable after it. Set it to "off" to decline vector memory entirely.
func GetVectorDBSource() string {
	if v := strings.TrimSpace(getEnvVarQuiet("DB_VECTOR_SOURCE")); v != "" {
		return v
	}
	return sqliteSourceDefault()
}

// sqliteSourceDefault is the sqlite file this deployment uses, or would use:
// DB_SOURCE when it names a sqlite database, and the stock path otherwise (so a
// deployment configured straight onto postgres still has a sane sqlite path for
// its vector indexes and its saved selection).
func sqliteSourceDefault() string {
	if GetDBDriver() == "sqlite" {
		return GetDBSource()
	}
	return "db/flomorphic.db"
}

// GetLocalEntities names the entities that stay in the sqlite file even when the
// main driver is postgres (DB_LOCAL_TABLES), as a comma-separated list of entity
// keys — see repository/postgres.LocalEntityKeys. The convenience value "config"
// expands to the two credential-bearing ones (node settings and the Connect
// gateways), so an operator can keep keys on the local disk while the records go
// to postgres. Empty (the default) means postgres carries everything.
func GetLocalEntities() []string {
	raw := strings.TrimSpace(getEnvVarQuiet("DB_LOCAL_TABLES"))
	if raw == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if p := strings.ToLower(strings.TrimSpace(part)); p != "" {
			out = append(out, p)
		}
	}
	return out
}
