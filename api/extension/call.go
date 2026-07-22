package extensionControllers

import (
	"encoding/json"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	InfraSpaces "github.com/Inflowenger/inflow-fusion/spaces"
	"github.com/gofiber/fiber/v3"
)

// callPluginFn handles POST /extension/id/:id/:fn — a REST->inflowv1 shim. It
// proxies a call to the plugin metaFunc `inflow.v1.<id>.<fn>` over NATS and
// returns the plugin's raw JSON response inside the standard envelope.
//
// Unlike the descriptor GETs (intro/settings/actions/form), `:id` here is the
// plugin's inflowv1 PluginID *directly* — e.g. the builtin MCP node's hard-coded
// seed id `aaaa-bbbb-cccc-mcp0` — not an extension-table row id, so no DB lookup
// is needed. That is what lets the front end reach any protocol/meta method of a
// plugin by its known pluginId (e.g. the MCP "load tools" button calls
// `.../getToolsList` so the plugin connects and enumerates its tools).
//
// Subject construction (the `inflow.v1.` prefix and versioning) lives entirely in
// inflow-fusion's DefaultPluginMetaFunc so future protocol versions are handled
// there rather than in every caller. The raw request body is forwarded to the
// plugin (the MCP node sends {url, transport, auth}).
func (ctl *controller) callPluginFn(c fiber.Ctx) error {
	pluginID := strings.TrimSpace(c.Params("id"))
	fn := strings.TrimSpace(c.Params("fn"))
	if pluginID == "" || fn == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "plugin id and function are required")
	}
	raw, err := InfraSpaces.DefaultPluginMetaFunc(pluginID, fn, c.Body())
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}
	return etc.OK(c, json.RawMessage(raw))
}
