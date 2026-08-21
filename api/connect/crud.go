package connectControllers

import (
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/openconnector"
	"github.com/gofiber/fiber/v3"
)

// list handles GET /connect/connections — every configured gateway connection,
// tokens masked, default first.
func (ctl *controller) list(c fiber.Ctx) error {
	items, err := ctl.repo.List(c.Context())
	if err != nil {
		return etc.Fail(c, fiber.StatusInternalServerError, err.Error())
	}
	out := make([]models.ConnectConnection, 0, len(items))
	for _, it := range items {
		out = append(out, it.Sanitized())
	}
	return etc.OK(c, out)
}

// upsert handles POST /connect/connections — create (no id) or update. On update
// an empty token keeps the stored one, so the client never has to re-send the
// secret just to rename a connection or flip its default.
func (ctl *controller) upsert(c fiber.Ctx) error {
	var input models.ConnectConnection
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid connection payload")
	}
	input.Label = strings.TrimSpace(input.Label)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.Token = strings.TrimSpace(input.Token)
	input.AdminToken = strings.TrimSpace(input.AdminToken)
	input.Kind = strings.TrimSpace(input.Kind)
	if input.Label == "" {
		input.Label = "OpenConnector"
	}
	if input.BaseURL == "" {
		input.BaseURL = openconnector.HostedBaseURL
	}

	// An empty token on update keeps the stored one — the repository preserves it.
	if err := ctl.repo.Upsert(c.Context(), &input); err != nil {
		return etc.FailFromRepo(c, err, "connection not found")
	}
	return etc.OK(c, input.Sanitized())
}

// getByID handles GET /connect/connections/id/:id.
func (ctl *controller) getByID(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "connection not found")
	}
	return etc.OK(c, rec.Sanitized())
}

// deleteByID handles DELETE /connect/connections/id/:id.
func (ctl *controller) deleteByID(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.Delete(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "connection not found")
	}
	return etc.Send(c, fiber.StatusAccepted, fiber.Map{"id": id}, nil)
}

// setDefault handles POST /connect/connections/id/:id/default.
func (ctl *controller) setDefault(c fiber.Ctx) error {
	id := c.Params("id")
	if err := ctl.repo.SetDefault(c.Context(), id); err != nil {
		return etc.FailFromRepo(c, err, "connection not found")
	}
	rec, err := ctl.repo.GetByID(c.Context(), id)
	if err != nil {
		return etc.FailFromRepo(c, err, "connection not found")
	}
	return etc.OK(c, rec.Sanitized())
}

// testInline handles POST /connect/connections/test — probe an ad-hoc
// {baseUrl, token, adminToken} against the gateway before the user saves it.
func (ctl *controller) testInline(c fiber.Ctx) error {
	var input struct {
		BaseURL    string `json:"baseUrl"`
		Token      string `json:"token"`
		AdminToken string `json:"adminToken"`
	}
	if err := c.Bind().Body(&input); err != nil {
		return etc.Fail(c, fiber.StatusBadRequest, "invalid payload")
	}
	return ctl.ping(c, input.BaseURL, input.Token, input.AdminToken)
}

// testStored handles POST /connect/connections/id/:id/test — probe a stored
// connection using its saved tokens.
func (ctl *controller) testStored(c fiber.Ctx) error {
	rec, err := ctl.repo.GetByID(c.Context(), c.Params("id"))
	if err != nil {
		return etc.FailFromRepo(c, err, "connection not found")
	}
	return ctl.ping(c, rec.BaseURL, rec.Token, rec.AdminToken)
}

// ping probes each token against the surface it authenticates: the admin token
// against `/api/connections` (management) and the runtime token against
// `/v1/actions` (execution). The connection is reachable if either succeeds; the
// result reports which surface answered.
func (ctl *controller) ping(c fiber.Ctx, baseURL, runtimeToken, adminToken string) error {
	runtimeToken = strings.TrimSpace(runtimeToken)
	adminToken = strings.TrimSpace(adminToken)
	if runtimeToken == "" && adminToken == "" {
		return etc.Fail(c, fiber.StatusBadRequest, "a token is required to reach the gateway")
	}

	var base string
	adminOK, runtimeOK := false, false
	var lastErr error

	if adminToken != "" {
		client := openconnector.New(baseURL, adminToken)
		base = client.BaseURL
		if err := client.Probe(c.Context(), "/api/connections"); err != nil {
			lastErr = err
		} else {
			adminOK = true
		}
	}
	if runtimeToken != "" {
		client := openconnector.New(baseURL, runtimeToken)
		base = client.BaseURL
		if err := client.Probe(c.Context(), "/v1/actions"); err != nil {
			lastErr = err
		} else {
			runtimeOK = true
		}
	}

	if !adminOK && !runtimeOK {
		return etc.Fail(c, fiber.StatusBadGateway, lastErr.Error())
	}
	return etc.OK(c, fiber.Map{"ok": true, "baseUrl": base, "adminOk": adminOK, "runtimeOk": runtimeOK})
}
