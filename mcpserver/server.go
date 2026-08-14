// Package mcpserver exposes the FloMorphic API to MCP clients (Claude, etc.) as
// an embedded Streamable HTTP endpoint mounted on the existing Fiber app at
// /mcp. It runs in-process over the same repository.Store, inflow runtime and
// inflow/svc embedding helpers the HTTP controllers use, so every tool is a thin
// wrapper over the exact call path a REST handler would take — no logic is
// duplicated and an MCP write is indistinguishable from the equivalent REST
// write.
//
// The tools mirror the REST surface: reads and writes for every entity, vector
// search / read-only doc-store SQL over memory stores, and the runtime actions
// (start/stop a run, close a human task) that a full-control client is trusted
// with. See tools_*.go for the per-entity registrations.
package mcpserver

import (
	"github.com/FloMorphic/morph-api/repository"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "flomorphic"
	serverVersion = "0.1.0"
	// endpointPath is where the MCP transport is mounted. ServeHTTP itself is
	// path-agnostic (it routes on HTTP method), so this only needs to match the
	// Fiber mount below and the URL clients point at.
	endpointPath = "/mcp"
)

// Register builds the MCP server, registers every entity's tools over the store,
// and mounts the Streamable HTTP transport at /mcp on the Fiber app. Called from
// api.RegisterAll behind env.MCPEnabled(); when AuthEnabled() is on it is mounted
// after the auth gate, so /mcp inherits the same bearer protection as the CRUD
// groups.
func Register(app fiber.Router, store repository.Store) {
	s := server.NewMCPServer(
		serverName, serverVersion,
		server.WithToolCapabilities(true),
		// The designer surface (tools_designer.go) exposes the workflow-designer
		// instructions as an MCP prompt, so the prompt capability is advertised.
		server.WithPromptCapabilities(true),
		server.WithRecovery(),
	)

	registerWorkflowTools(s, store)
	registerDesignerTools(s, store)
	registerContextTools(s, store)
	registerProcessTools(s, store)
	registerPromptTools(s, store)
	registerMemoryTools(s, store)
	registerHumanTaskTools(s, store)
	registerSettingsTools(s, store)
	registerExtensionTools(s, store)
	registerTriggerTools(s, store)

	// Stateless: each POST is a self-contained JSON-RPC exchange with no
	// server-held session, which is the simplest shape to serve through Fiber's
	// http.Handler adaptor (no long-lived SSE stream to keep alive across the
	// bridge). Clients that expect a session id still work — none is required.
	h := server.NewStreamableHTTPServer(s,
		server.WithEndpointPath(endpointPath),
		server.WithStateLess(true),
	)

	handler := adaptor.HTTPHandler(h)
	app.All(endpointPath, handler)
}
