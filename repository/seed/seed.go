// Package seed holds the first-run seed data shared by every repository
// backend: the builtin palette nodes and the starter prompt templates.
//
// The JSON lives here, not inside one backend, because seeding is a property of
// the product rather than of a database: whichever driver a deployment opens
// (sqlite, postgres, …) must come up with the same palette and the same starter
// prompts. Each backend calls Builtins()/Prompts() from its Open and applies
// them through its own idempotent seeding method.
package seed

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/FloMorphic/morph-api/models"
	"github.com/bytedance/sonic"
)

//go:embed builtins.json
var builtinsJSON []byte

//go:embed prompts.json
var promptsJSON []byte

// Builtins parses the embedded builtin palette seed. An empty file yields no
// definitions and no error — a deployment may legitimately ship without one.
func Builtins() ([]models.ExtensionRecord, error) {
	var defs []models.ExtensionRecord
	if len(builtinsJSON) == 0 {
		return defs, nil
	}
	if err := sonic.Unmarshal(builtinsJSON, &defs); err != nil {
		return nil, fmt.Errorf("decode builtins seed: %w", err)
	}
	return defs, nil
}

// Prompts parses the embedded prompt-template seed. Each definition carries a
// fixed id, which is what makes re-seeding idempotent.
func Prompts() ([]models.PromptRecord, error) {
	var defs []models.PromptRecord
	if len(promptsJSON) == 0 {
		return defs, nil
	}
	if err := sonic.Unmarshal(promptsJSON, &defs); err != nil {
		return nil, fmt.Errorf("decode prompts seed: %w", err)
	}
	return defs, nil
}

// RetiredBuiltinNames are the pre-morphic generic builtins the current seed
// replaced. Backends prune them while seeding so an existing database's palette
// converges on the seeded set (idempotent — a missing row is a no-op).
//
// A name here must NOT also appear in builtins.json: the prune runs before the
// insert pass, so a name in both lists is deleted and re-seeded on every boot —
// which silently discards any admin edit to that node and changes its row id
// each time. ("Goto" was in both and is deliberately not listed here.)
var RetiredBuiltinNames = []string{"PluginNative", "Code", "Contract", "Extrinsics", "Void"}

// ExtensionSeeder is the part of repository.ExtensionRepository seeding needs.
type ExtensionSeeder interface {
	SeedBuiltins(ctx context.Context, defs []models.ExtensionRecord) (int, error)
}

// PromptSeeder is the part of repository.PromptRepository seeding needs.
type PromptSeeder interface {
	SeedPrompts(ctx context.Context, defs []models.PromptRecord) (int, error)
}

// Apply runs the shared first-run seeding against one backend's seeding
// methods, reporting what it did on stdout. A bad or empty seed is a warning,
// never a startup error: the API must still serve CRUD.
func Apply(ctx context.Context, ext ExtensionSeeder, prompts PromptSeeder) {
	if defs, err := Builtins(); err != nil {
		fmt.Printf("warning: builtins seed not applied: %v\n", err)
	} else if n, err := ext.SeedBuiltins(ctx, defs); err != nil {
		fmt.Printf("warning: builtins seed not applied: %v\n", err)
	} else if n > 0 {
		fmt.Printf("seeded %d builtin node(s)\n", n)
	}

	if defs, err := Prompts(); err != nil {
		fmt.Printf("warning: prompts seed not applied: %v\n", err)
	} else if n, err := prompts.SeedPrompts(ctx, defs); err != nil {
		fmt.Printf("warning: prompts seed not applied: %v\n", err)
	} else if n > 0 {
		fmt.Printf("seeded %d prompt(s)\n", n)
	}
}
