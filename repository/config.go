package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config names a backend and its data source — the whole of "which database is
// this deployment on". It is what the environment supplies at boot and what the
// database panel overwrites when an operator points FloMorphic at a postgres.
type Config struct {
	// Driver is a registered backend name ("sqlite", "postgres").
	Driver string `json:"driver"`
	// Source is the driver-specific data source: a file path for sqlite, a URI
	// (postgres://…) or key/value DSN for postgres.
	Source string `json:"source"`
	// UpdatedAt is when this config was saved, epoch millis. Zero for a config
	// that came from the environment rather than the panel.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

// Normalized trims the pair, fills in the default driver and repairs a pasted
// connection URI whose password was not percent-encoded, so a panel payload
// that names only a source still opens.
func (c Config) Normalized() Config {
	c.Driver = strings.ToLower(strings.TrimSpace(c.Driver))
	c.Source = normalizeURISource(strings.TrimSpace(c.Source))
	if c.Driver == "" {
		c.Driver = DefaultDriver
	}
	// postgres is reachable under several spellings in the wild; accept them all
	// and settle on the registered one.
	switch c.Driver {
	case "postgresql", "pgsql", "pg":
		c.Driver = "postgres"
	case "sqlite3":
		c.Driver = "sqlite"
	}
	return c
}

// Validate reports whether the config can be opened: a registered driver and,
// for anything but sqlite, a non-empty source (sqlite falls back to a default
// file, postgres cannot guess a server).
func (c Config) Validate() error {
	c = c.Normalized()
	if !HasDriver(c.Driver) {
		return fmt.Errorf("unknown database driver %q (available: %s)", c.Driver, strings.Join(Drivers(), ", "))
	}
	if c.Driver != "sqlite" && c.Source == "" {
		return fmt.Errorf("driver %q needs a connection URI", c.Driver)
	}
	return nil
}

// DefaultDriver is the backend a deployment gets when nothing selects one.
// SQLite is enough for a single-node install and needs no server.
const DefaultDriver = "sqlite"

// Redacted returns a copy safe to hand to a client: the source keeps its shape
// (so the panel can show which server it points at) but any embedded password
// is masked. Credentials go in write-only, exactly like the Connect tokens.
func (c Config) Redacted() Config {
	c.Source = RedactSource(c.Source)
	return c
}

// dsnPasswordRe matches the password field of a libpq key/value DSN
// ("host=… password=secret …"), quoted or bare.
var dsnPasswordRe = regexp.MustCompile(`(?i)(\bpassword\s*=\s*)('[^']*'|"[^"]*"|\S+)`)

// uriPasswordRe matches the password in a URI's userinfo
// ("scheme://user:secret@host/…"). The mask is spliced into the raw string
// rather than going through net/url, whose re-encoding would turn the mask
// itself into %2A%2A%2A. The password part is greedy and the user part lazy, so
// the split lands on the LAST '@' of the authority — the same rule every URI
// parser uses, and the one that keeps a raw '@' inside a password masked
// instead of half-exposed.
var uriPasswordRe = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.\-]*://[^/?#]*?:)[^/?#]*@`)

// normalizeURISource percent-encodes the password of a connection URI whose
// userinfo holds characters that must be escaped — in practice an unencoded '@',
// which operators paste constantly because it is what their password manager
// shows them.
//
// Left alone, such a URI does not fail cleanly: every parser splits the
// authority on its last '@', so `postgres://user:pa@ss@host:5432/db` is read as
// host `ss@host` and the connection dies with "hostname resolving error: lookup
// ss@host", which names nothing the operator typed. Rewriting it is not a guess:
// the last '@' is the host separator by definition, so everything between the
// first ':' and it is the password, whatever it contains.
//
// Only a URI whose userinfo still holds an '@' after that split is touched —
// one that is already well-formed is returned byte for byte, so an escape the
// operator wrote themselves is never double-encoded.
func normalizeURISource(s string) string {
	i := strings.Index(s, "://")
	if i < 0 {
		return s // a file path (sqlite), or a key/value DSN
	}
	head, rest := s[:i+3], s[i+3:]
	// The authority ends at the first '/', '?' or '#'.
	end := len(rest)
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		end = j
	}
	authority, tail := rest[:end], rest[end:]

	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return s // no userinfo
	}
	userinfo, host := authority[:at], authority[at:]
	if !strings.Contains(userinfo, "@") {
		return s // already well-formed
	}
	colon := strings.Index(userinfo, ":")
	if colon < 0 {
		// An '@' in the username with no password: which '@' separates what is
		// genuinely ambiguous, so leave it and let the driver report.
		return s
	}
	return head + userinfo[:colon+1] + encodeUserinfo(userinfo[colon+1:]) + host + tail
}

// encodeUserinfo percent-encodes the characters a URI's userinfo may not carry
// raw. '%' is left as-is so an operator's own escapes survive untouched, and
// ':' is left as-is because the password runs to the end of the userinfo and
// every parser accepts it there.
func encodeUserinfo(s string) string {
	const safe = "-._~!$&'()*+,;=:%"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			strings.IndexByte(safe, ch) >= 0:
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// RedactSource masks the password in a data source, leaving everything else
// readable. It handles the three shapes a source arrives in: a URI with
// userinfo, a libpq key/value DSN, and a plain file path (nothing to mask).
func RedactSource(source string) string {
	s := strings.TrimSpace(source)
	if s == "" {
		return s
	}
	s = uriPasswordRe.ReplaceAllString(s, "${1}***@")
	return dsnPasswordRe.ReplaceAllString(s, "${1}***")
}

// LoadConfig reads a config file written by SaveConfig. A missing file is not an
// error: it means "nothing has overridden the environment", and the caller falls
// back to its own defaults. A file that exists but cannot be parsed IS an error
// — silently booting onto a different database than the operator selected is
// worse than refusing to start.
func LoadConfig(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("repository: read db config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("repository: parse db config %q: %w", path, err)
	}
	cfg = cfg.Normalized()
	if cfg.Source == "" && cfg.Driver == DefaultDriver {
		// An empty file is the same as no file.
		return nil, nil
	}
	return &cfg, nil
}

// SaveConfig writes the selected database to path, creating its directory. The
// file holds the connection URI — password included, because the process must
// be able to reconnect after a restart without the operator retyping it — so it
// is written 0600, like any other credential file.
func SaveConfig(path string, cfg Config) (Config, error) {
	cfg = cfg.Normalized()
	cfg.UpdatedAt = time.Now().UnixMilli()
	if strings.TrimSpace(path) == "" {
		return cfg, errors.New("repository: no db config file configured")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return cfg, fmt.Errorf("repository: create db config dir %q: %w", dir, err)
		}
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return cfg, fmt.Errorf("repository: encode db config: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return cfg, fmt.Errorf("repository: write db config %q: %w", path, err)
	}
	return cfg, nil
}

// RemoveConfig deletes the saved selection, so the next boot (and the switch
// that follows this call) falls back to the environment's database. A file that
// is already gone is a success — the end state is what was asked for.
func RemoveConfig(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("repository: remove db config %q: %w", path, err)
	}
	return nil
}
