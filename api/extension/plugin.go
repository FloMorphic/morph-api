package extensionControllers

import (
	"errors"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/env"
	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	fuse "github.com/Inflowenger/inflow-fusion/inflow"
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
	cred, err := mintCred(input)
	if err != nil {
		return credError(c, err)
	}
	dotenv := pluginEnvFile(input.PluginId, cred, nil)
	return etc.OK(c, fiber.Map{"env": dotenv, "cred": cred})
}

// errBadCredRequest marks a caller mistake (missing field / unknown access) so
// the handlers can answer 400 instead of 500 without inspecting strings.
var errBadCredRequest = errors.New("bad credential request")

// mintCred resolves the signing account and mints the credential described by
// the request. Split out of the handler because the installer endpoints mint the
// very same credential to bake into the env file they generate.
func mintCred(input models.CredRequest) (string, error) {
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
			return "", fmt.Errorf("%w: pluginId is required", errBadCredRequest)
		}
		if input.Name == "" {
			input.Name = input.PluginId
		}
	case models.MultiPluginAccess:
		if input.Name == "" {
			return "", fmt.Errorf("%w: name is required", errBadCredRequest)
		}
	default:
		return "", fmt.Errorf("%w: access must be 'strict' or 'multi'", errBadCredRequest)
	}

	// Resolve the account whose seed signs the credential: the builtin-plugins
	// account by default, or an explicit space.
	var spaceSeed string
	if input.SpaceId == "" {
		acc, err := InfraSpaces.GetPluginBuiltinAccount()
		if err != nil {
			return "", err
		}
		spaceSeed = acc.Seed
	} else {
		acc, err := InfraSpaces.GetAccountById(input.SpaceId)
		if err != nil {
			return "", err
		}
		spaceSeed = acc.Seed
	}

	kp, err := nkeys.FromSeed([]byte(spaceSeed))
	if err != nil {
		return "", errors.New("invalid account keys")
	}
	pub, _ := kp.PublicKey()

	var perm = InfraSpaces.PluginCredentialStrictPermission(input.Name, input.PluginId, pub)
	if input.Access == models.MultiPluginAccess {
		perm = InfraSpaces.PluginCredentialOpenPermission(input.Name, pub)
	}
	ucred, err := InfraSpaces.CreateUserCredential(spaceSeed, perm)
	if err != nil {
		return "", errors.New("error occurred in create access token")
	}
	return ucred.Base64Cred, nil
}

// credError maps a mintCred failure to its HTTP status: a caller mistake is a
// 400, an unreachable/ misconfigured infra a 500.
func credError(c fiber.Ctx, err error) error {
	if errors.Is(err, errBadCredRequest) {
		return etc.Fail(c, fiber.StatusBadRequest, strings.TrimPrefix(err.Error(), errBadCredRequest.Error()+": "))
	}
	return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
}

// pluginEnvFile renders the dotenv a plugin process reads: the three variables
// every inflowv1 SDK plugin needs, followed by whatever extra variables the
// extension declared (upstream API keys, endpoints, …).
//
// The three reserved keys are emitted exactly once, so a declared extra never
// duplicates them. A declared INFRA_URL overrides the address this API derived:
// the operator knows the host their plugin can actually reach Infra on, which
// this API often can't (compose-internal "infra:4222" vs. the published one).
// PLUGIN_ID and INFRA_CRED stay authoritative — a backend-issued identity and a
// freshly minted secret — so a declared value for either is ignored, never
// allowed to clobber them.
func pluginEnvFile(pluginID, cred string, extra []models.EnvVar) string {
	infraURL := infraNatsURL()
	for _, e := range extra {
		if strings.TrimSpace(e.Key) == "INFRA_URL" && strings.TrimSpace(e.Value) != "" {
			infraURL = e.Value
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "PLUGIN_ID=%s\n", pluginID)
	fmt.Fprintf(&b, "INFRA_URL=%s\n", infraURL)
	fmt.Fprintf(&b, "INFRA_CRED=%s\n", cred)

	reserved := map[string]bool{"PLUGIN_ID": true, "INFRA_URL": true, "INFRA_CRED": true}
	for _, e := range extra {
		key := strings.TrimSpace(e.Key)
		if key == "" || reserved[key] {
			continue
		}
		fmt.Fprintf(&b, "%s=%s\n", key, e.Value)
	}
	return b.String()
}

// infraNatsURL is the NATS endpoint written into a plugin's INFRA_URL. An
// explicit PLUGIN_INFRA_URL always wins — the address this API reaches Infra on
// (typically the compose-internal "infra:4222") is often not the one a plugin
// running on someone's laptop can use. Otherwise it reports whatever the
// connected runtime dialled, and falls back to the compose default when the
// runtime is off.
func infraNatsURL() string {
	if u := env.GetInfraNatsUrl(); u != "" {
		return u
	}
	if backend := fuse.GetInflowBackend(); backend != nil {
		if u := strings.TrimSpace(backend.GetInfraNatsUrl()); u != "" {
			return u
		}
	}
	return "infra:4222"
}
