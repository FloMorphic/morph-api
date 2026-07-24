package extensionControllers

import (
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	InfraSpaces "github.com/Inflowenger/inflow-fusion/spaces"
	"github.com/gofiber/fiber/v3"
	"github.com/nats-io/nkeys"
)

// pluginCred handles POST /extension/plugin/cred — mint a runtime credential for
// a plugin-backed node so its inflowv1 plugin can be run to serve the node's
// functionality. The builtin llm/mcp/cast nodes carry a hard-coded PluginID from
// the seed; the front end sends it here (via the "get credential" button on the
// settings page / builtin node) to obtain the credential + env for the plugin.
//
// Mirrors inspector-api's getPluginCred: it resolves the target account (the
// builtin-plugins account when SpaceId is empty), then mints an open (multi) or
// plugin-scoped (strict) user credential.
func (ctl *controller) pluginCred(c fiber.Ctx) error {
	var input models.CredRequest
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid credential request")
	}
	input.PluginId = strings.TrimSpace(input.PluginId)
	input.Name = strings.TrimSpace(input.Name)
	if input.Access == "" {
		input.Access = models.StrictAccess
	}
	// strict credentials are scoped to a single plugin's subjects, so they need
	// a pluginId; open (multi) credentials are account-wide and only need a name.
	switch input.Access {
	case models.StrictAccess:
		if input.PluginId == "" {
			return etc.Fail(c, fiber.StatusBadRequest, "pluginId is required")
		}
	case models.MultiPluginAccess:
		if input.Name == "" {
			return etc.Fail(c, fiber.StatusBadRequest, "name is required")
		}
	}

	// Resolve the account whose seed signs the credential: the builtin-plugins
	// account by default, or an explicit space.
	var spaceSeed string
	if input.SpaceId == "" {
		acc, err := InfraSpaces.GetPluginBuiltinAccount()
		if err != nil {
			return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
		}
		spaceSeed = acc.Seed
	} else {
		acc, err := InfraSpaces.GetAccountById(input.SpaceId)
		if err != nil {
			return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
		}
		spaceSeed = acc.Seed
	}

	kp, err := nkeys.FromSeed([]byte(spaceSeed))
	if err != nil {
		return etc.Fail(c, fiber.StatusNotAcceptable, "invalid account keys")
	}
	pub, _ := kp.PublicKey()

	var cred string
	switch input.Access {
	case models.MultiPluginAccess:
		ucred, err := InfraSpaces.CreateUserCredential(spaceSeed, InfraSpaces.PluginCredentialOpenPermission(input.Name, pub))
		if err != nil {
			return etc.Fail(c, fiber.StatusNotAcceptable, "error occurred in create access token")
		}
		cred = ucred.Base64Cred
	case models.StrictAccess:
		ucred, err := InfraSpaces.CreateUserCredential(spaceSeed, InfraSpaces.PluginCredentialStrictPermission(input.Name, input.PluginId, pub))
		if err != nil {
			return etc.Fail(c, fiber.StatusNotAcceptable, "error occurred in create access token")
		}
		cred = ucred.Base64Cred
	default:
		return etc.Fail(c, fiber.StatusBadRequest, "access must be 'strict' or 'multi'")
	}

	env := fmt.Sprintf("INFRA_CRED=%s\nINFRA_URL=%s\nPLUGIN_ID=%s\n", cred, "infra:4222", input.PluginId)
	return etc.OK(c, fiber.Map{"env": env, "cred": cred})
}
