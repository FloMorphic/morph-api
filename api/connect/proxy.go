package connectControllers

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/FloMorphic/morph-api/etc"
	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/openconnector"
	"github.com/gofiber/fiber/v3"
)

// connectionHeader / connectionQuery name the connection a proxied call targets.
// Either overrides the default connection; the query key is stripped before the
// remaining query string is forwarded to the gateway.
const (
	connectionHeader = "X-Connection-Id"
	connectionQuery  = "__connection"
)

// proxy handles ALL /connect/gateway/* — an authenticated passthrough to the
// resolved OpenConnector gateway. The wildcard sub-path is forwarded verbatim
// (so `/connect/gateway/api/providers` hits `<baseUrl>/api/providers`), the
// stored bearer token is injected, and the gateway's JSON is relayed inside the
// standard `{ data, error }` envelope.
func (ctl *controller) proxy(c fiber.Ctx) error {
	conn, err := ctl.resolve(c)
	if err != nil {
		return etc.FailFromRepo(c, err, "no OpenConnector connection configured")
	}

	sub := c.Params("*")
	if !strings.HasPrefix(sub, "/") {
		sub = "/" + sub
	}

	// The management surface (/api, /docs) needs the admin token; execution
	// (/v1, /mcp) needs the runtime token. Pick per path.
	token, need := tokenForPath(conn, sub)
	if strings.TrimSpace(token) == "" {
		return etc.Fail(c, fiber.StatusPreconditionFailed,
			"the selected connection has no "+need+" token (management endpoints need the self-hosted admin token)")
	}

	// Forward the query string minus our connection selector.
	query := url.Values{}
	for k, v := range c.Queries() {
		if k == connectionQuery {
			continue
		}
		query.Set(k, v)
	}

	var body *bytes.Reader
	raw := c.Body()
	if len(raw) > 0 {
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}

	client := openconnector.New(conn.BaseURL, token)
	res, err := client.Do(c.Context(), c.Method(), sub, query, body)
	if err != nil {
		return etc.Fail(c, fiber.StatusBadGateway, err.Error())
	}

	data := decodeBody(res)
	if res.Status >= 400 {
		return etc.Send(c, res.Status, nil, models.ErrorResponse{
			Code:    res.Status,
			Message: gatewayMessage(data, res.Status),
		})
	}
	return etc.Send(c, res.Status, data, nil)
}

// tokenForPath selects which stored token authenticates a gateway path and names
// it for error messages. The management surface (/api, /docs) uses the admin
// token; execution (/v1, /mcp) and anything else uses the runtime token. When a
// self-hosted admin token is not stored, management calls fall back to the
// runtime token — the gateway then answers (or rejects) authoritatively.
func tokenForPath(conn *models.ConnectConnection, path string) (token, need string) {
	if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/docs") {
		if strings.TrimSpace(conn.AdminToken) != "" {
			return conn.AdminToken, "admin"
		}
		return conn.Token, "admin"
	}
	return conn.Token, "runtime"
}

// resolve picks the connection a proxied call targets: the header/query override
// if present, otherwise the default connection.
func (ctl *controller) resolve(c fiber.Ctx) (*models.ConnectConnection, error) {
	id := strings.TrimSpace(c.Get(connectionHeader))
	if id == "" {
		id = strings.TrimSpace(c.Query(connectionQuery))
	}
	if id != "" {
		return ctl.repo.GetByID(c.Context(), id)
	}
	return ctl.repo.Default(c.Context())
}

// decodeBody turns the gateway response into something JSON-serialisable: the
// raw JSON when the body parses, otherwise the body as a string.
func decodeBody(res *openconnector.Response) any {
	trimmed := bytes.TrimSpace(res.Body)
	if len(trimmed) == 0 {
		return nil
	}
	if strings.Contains(res.ContentType, "json") || looksJSON(trimmed) {
		if json.Valid(trimmed) {
			return json.RawMessage(trimmed)
		}
	}
	return string(trimmed)
}

func looksJSON(b []byte) bool {
	return b[0] == '{' || b[0] == '[' || b[0] == '"'
}

// gatewayMessage pulls a human-readable error out of a gateway error body,
// falling back to a generic status message.
func gatewayMessage(data any, status int) string {
	if raw, ok := data.(json.RawMessage); ok {
		var obj map[string]any
		if json.Unmarshal(raw, &obj) == nil {
			for _, key := range []string{"message", "error", "detail"} {
				if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
					return v
				}
			}
		}
	}
	if s, ok := data.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return "gateway request failed"
}
