package models

// Database selection DTOs — the wire shape of the admin panel's "which database
// is this instance on" screen. They are plain structs here (rather than the
// repository's own types) so the models package stays free of any dependency on
// the persistence layer, and so the web app has one JSON contract to type
// against.

// DatabaseTarget is a database to test or switch to: a driver plus its
// driver-specific source. For sqlite the source is a file path, for postgres a
// connection URI (postgres://user:pass@host:5432/db) or a libpq key/value DSN.
// A blank driver means the default, sqlite.
type DatabaseTarget struct {
	Driver string `json:"driver"`
	Source string `json:"source"`
}

// DatabaseState is the panel's whole view of persistence: which database is
// live, where the selection came from, whether it is answering, and what else
// could be selected.
type DatabaseState struct {
	// Driver and Source identify the live database. Source is redacted — any
	// password in it is replaced with ***, because this travels to a browser.
	Driver string `json:"driver"`
	Source string `json:"source"`
	// Origin is "panel" when the live selection came from the saved config file
	// and "env" when it came from DB_DRIVER/DB_SOURCE. Resetting the selection
	// returns an instance to "env".
	Origin string `json:"origin"`
	// UpdatedAt is when the panel selection was saved, epoch millis. Zero when
	// the database came from the environment.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
	// Drivers lists the backends this build can open.
	Drivers []string `json:"drivers"`
	// Switchable is whether this instance can be repointed at run time. False
	// means the selection is fixed for the process's lifetime (the endpoints
	// then only report).
	Switchable bool `json:"switchable"`
	// Connected and LatencyMs are a live health check of the database; Error
	// carries the reason when it is not answering.
	Connected bool   `json:"connected"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
	// VectorSource is the sqlite file holding the vector indexes. Vector memory
	// is sqlite-vec on every backend, so this stays a file path even on
	// postgres; empty means vector memory is switched off.
	VectorSource string `json:"vectorSource,omitempty"`
	// LocalEntities names the entities a non-sqlite deployment still serves from
	// that sqlite file (DB_LOCAL_TABLES) — typically the credential-bearing
	// ones. Empty means the live driver carries everything.
	LocalEntities []string `json:"localEntities,omitempty"`
	// EntityKeys is every value LocalEntities accepts, for the panel to offer.
	EntityKeys []string `json:"entityKeys,omitempty"`
	// ConfigFile is where a panel selection is persisted, so an operator can
	// find (or delete) it on disk.
	ConfigFile string `json:"configFile,omitempty"`
}

// DatabaseProbe is what a test of a candidate database found. A probe never
// creates application tables, so a failed test leaves the target untouched.
type DatabaseProbe struct {
	// OK is whether the candidate can be used. Error explains a false.
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Driver string `json:"driver"`
	// Server identifies what answered ("PostgreSQL 16.2", a sqlite file).
	Server string `json:"server,omitempty"`
	// Writable is whether this connection may actually create objects — a
	// read-only replica opens fine and then fails on the first save.
	Writable bool `json:"writable"`
	// Vector is whether vector memory will work after switching.
	Vector bool `json:"vector"`
	// Notes are human-readable findings to show verbatim.
	Notes []string `json:"notes,omitempty"`
}
