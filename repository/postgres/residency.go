package postgres

import (
	"fmt"
	"sort"
	"strings"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/repository"
)

// Entity residency: which tables a postgres deployment keeps in sqlite anyway.
//
// Postgres carries every entity by default — the backend is a full
// implementation, not a partial one. But not every table wants to move. The
// credential-bearing ones (the node settings profiles and the OpenConnector
// gateways) are a deployment's keys rather than its records: an operator may
// well prefer them to stay on the local disk, in the file they already back up,
// instead of being copied into a shared database. Vector indexes are the same
// argument taken to its conclusion and are not optional — they are always
// sqlite-vec (see the package comment).
//
// DB_LOCAL_TABLES names the entities to keep local. Each named entity is served
// by a sqlite store opened on the vector sidecar's file, so one file holds
// everything that stayed behind.
const (
	EntityWorkflows    = "workflows"
	EntityContexts     = "contexts"
	EntityMemory       = "memory"
	EntityPrompts      = "prompts"
	EntityHumanTasks   = "hitl"
	EntityNodeSettings = "settings"
	EntityProcesses    = "processes"
	EntityExtensions   = "extensions"
	EntityTriggers     = "triggers"
	EntityConnect      = "connect"
	EntityTelegram     = "telegram"
)

// LocalEntityKeys is every entity DB_LOCAL_TABLES accepts, for error messages
// and for the database panel to offer.
var LocalEntityKeys = []string{
	EntityWorkflows, EntityContexts, EntityMemory, EntityPrompts, EntityHumanTasks,
	EntityNodeSettings, EntityProcesses, EntityExtensions, EntityTriggers,
	EntityConnect, EntityTelegram,
}

// configEntities is what the "config" shorthand expands to: the two entities
// that hold secrets rather than workflow data.
var configEntities = []string{EntityNodeSettings, EntityConnect}

// parseLocalEntities resolves the configured keys into a set, expanding the
// "config" and "all" shorthands. An unknown key is reported rather than ignored:
// a typo that silently sent a table to the wrong database would be found much
// later, by its absence.
func parseLocalEntities(keys []string) (map[string]bool, error) {
	known := map[string]bool{}
	for _, k := range LocalEntityKeys {
		known[k] = true
	}
	out := map[string]bool{}
	for _, raw := range keys {
		switch key := strings.ToLower(strings.TrimSpace(raw)); key {
		case "":
			continue
		case "none", "off":
			return map[string]bool{}, nil
		case "all":
			for _, k := range LocalEntityKeys {
				out[k] = true
			}
		case "config", "keys", "secrets":
			for _, k := range configEntities {
				out[k] = true
			}
		default:
			if !known[key] {
				return nil, fmt.Errorf("postgres: unknown DB_LOCAL_TABLES entity %q (known: %s)",
					key, strings.Join(LocalEntityKeys, ", "))
			}
			out[key] = true
		}
	}
	return out, nil
}

// localResidency is the set of entities served from sqlite plus the store that
// serves them. A zero value (no local store) sends everything to postgres.
type localResidency struct {
	store    repository.Store
	entities map[string]bool
	source   string
}

// holds reports whether an entity is served locally.
func (l *localResidency) holds(entity string) bool {
	return l != nil && l.store != nil && l.entities[entity]
}

// names lists the local entities, sorted, for the startup line and the panel.
func (l *localResidency) names() []string {
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.entities))
	for k := range l.entities {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// openLocalResidency opens the sqlite store backing the entities that stay
// local. It returns a nil residency when nothing was pinned — the common case,
// where postgres carries the lot and no second database is opened at all.
//
// Failing to open it is fatal to the switch rather than a warning: an operator
// who asked for their keys to stay in sqlite must not get a postgres that
// silently serves them from the wrong place.
func openLocalResidency(sidecar string) (*localResidency, error) {
	entities, err := parseLocalEntities(env.GetLocalEntities())
	if err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}
	if sidecar == "" || strings.EqualFold(sidecar, VectorDisabled) {
		return nil, fmt.Errorf("postgres: DB_LOCAL_TABLES needs a sqlite file; set DB_VECTOR_SOURCE")
	}
	st, err := repository.Open("sqlite", sidecar)
	if err != nil {
		return nil, fmt.Errorf("postgres: open local entity store: %w", err)
	}
	res := &localResidency{store: st, entities: entities, source: sidecar}
	fmt.Printf("postgres: %s kept in sqlite at %s\n", strings.Join(res.names(), ", "), sidecar)
	return res, nil
}

// close releases the local store, if any.
func (l *localResidency) close() error {
	if l == nil || l.store == nil {
		return nil
	}
	return l.store.Close()
}
