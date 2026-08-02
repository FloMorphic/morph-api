// Package extensionControllers is the HTTP controller for palette extensions
// (ExtensionRecord) — the node palette. Every row is a palette node: admin-
// managed builtins (kind=builtin, seeded on first run, UI hard-coded in the
// front end) and user-imported inflowv1 plugins (kind=extension, whose settings
// form and actions are fetched live over NATS). Full CRUD, page-paginated, with
// an optional `?kind=` filter, plus the extrinsic-services list and live
// inflowv1 proxy endpoints.
package extensionControllers

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
)

type controller struct {
	repo repository.ExtensionRepository
}

// Register mounts the /extension route group.
func Register(api fiber.Router, store repository.Store) {
	ctl := &controller{repo: store.Extensions()}

	g := api.Group("/extension")
	g.Post("", ctl.upsert)                  // create (no id) or update
	g.Get("", ctl.list)                     // list (?page=&per_page=&search=&kind=)
	g.Get("/extrinsics", ctl.extrinsics)    // backend extrinsic services to bind to
	g.Get("/id/:id", ctl.getByID)           // open one extension
	g.Delete("/id/:id", ctl.deleteByID)     // delete
	g.Get("/id/:id/intro", ctl.intro)       // live: plugin @intro
	g.Get("/id/:id/settings", ctl.settings) // live: plugin @settings form
	g.Get("/id/:id/actions", ctl.actions)   // live: plugin @actions list

	g.Get("/id/:id/actions/:method/form", ctl.actionForm) // live: one action's @form

	// Onboarding an imported plugin (see install.go): the one-liner + script that
	// installs and starts it from source, and the bare dotenv for a user who
	// already has the plugin on disk. Both mint the plugin's credential.
	g.Get("/id/:id/install", ctl.installInfo)
	g.Get("/id/:id/install.sh", ctl.installScriptRaw)
	g.Get("/id/:id/env", ctl.installEnv)

	// Rebuild a plugin's palette rows from its live @actions (see sync.go).
	// Registered before the catch-all POST below so "sync" is not read as a
	// plugin method name.
	g.Post("/id/:id/sync", ctl.syncActions)

	// Live REST->inflowv1 shim: POST a body to one of a plugin's methods (an
	// action or a metaFunc, e.g. the MCP node's getToolsList) and get its raw
	// response. Here `:id` is the plugin's inflowv1 PluginID *directly* (e.g. the
	// builtin MCP node's hard-coded seed id), not an extension row — no DB lookup.
	// The MCP "load tools" button POSTs {url, transport, auth} to
	// `/id/<mcpPluginId>/getToolsList`. Registered last so the more specific GETs
	// above win their paths.
	g.Post("/id/:id/:fn", ctl.callPluginFn) // live: call plugin inflow.v1.<id>.<fn>

	// Plugins: mint a runtime credential for a plugin-backed node (builtin
	// llm/mcp/cast carry a hard-coded pluginId from the seed) so its inflowv1
	// plugin can be run to serve the node's functionality.
	p := g.Group("/plugin")
	p.Post("/cred", ctl.pluginCred)
}
