// Package openconnector is a thin HTTP client for an OpenConnector gateway —
// oomol's hosted auth gateway (console.oomol.com) or a self-hosted instance. It
// is the single place FloMorphic reaches external SaaS providers through: it
// injects the stored bearer token and forwards requests, so callers (the Connect
// page today, plugin runs over NATS later) never handle provider credentials.
//
// The gateway already normalizes 1000+ providers behind a stable REST surface
// (`/api/*` for management — providers, connections, OAuth configs — and `/v1/*`
// for action execution). Rather than re-model every endpoint, this client offers
// one authenticated passthrough (Do) plus a couple of typed conveniences the UI
// needs, which keeps the whole gateway API reachable as it evolves.
package openconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HostedBaseURL is oomol's hosted OpenConnector API origin — the runtime/admin
// REST host (`connector.oomol.com`), NOT the web console at console.oomol.com
// (which only serves the UI and would 404 the /v1 and /api calls). Used as the
// default when a connection does not specify its own base URL.
const HostedBaseURL = "https://connector.oomol.com"

// Client talks to one OpenConnector gateway.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client for the given gateway. An empty baseURL falls back to the
// hosted service; a base pointing at the web console is corrected to the API host.
func New(baseURL, token string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = HostedBaseURL
	}
	// console.oomol.com is the web UI, not the API — it 404s /v1 and /api. A
	// connection saved against it (an easy mistake) is redirected to the API host
	// so it works without the user re-editing it.
	base = strings.Replace(base, "console.oomol.com", "connector.oomol.com", 1)
	return &Client{
		BaseURL: base,
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// authHeader renders the Authorization value for a credential. OpenConnector
// accepts two forms and the format depends on the credential type, so we do NOT
// blindly prepend "Bearer":
//   - a value that already carries a scheme ("Bearer …") is sent verbatim;
//   - a runtime token ("oct_…") is sent as "Bearer oct_…" (per the docs);
//   - anything else — notably an API key ("api-…") — is sent RAW, exactly as the
//     gateway expects (Authorization: api-…). Prepending Bearer here breaks it.
func authHeader(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(t), "bearer ") {
		return t
	}
	if strings.HasPrefix(t, "oct_") {
		return "Bearer " + t
	}
	return t
}

// Response is the raw result of a proxied call: the gateway's status code, body
// and content type, passed through untouched so the caller can relay them.
type Response struct {
	Status      int
	Body        []byte
	ContentType string
}

// Do performs an authenticated request against the gateway. `path` is the
// gateway path (e.g. "/api/providers" or "/v1/actions/github.list_repos"); it is
// appended to BaseURL. The bearer token is attached automatically.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body io.Reader) (*Response, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("openconnector: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h := authHeader(c.Token); h != "" {
		req.Header.Set("Authorization", h)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openconnector: request %s %s: %w", method, path, err)
	}
	defer res.Body.Close()

	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20)) // 16 MiB cap
	if err != nil {
		return nil, fmt.Errorf("openconnector: read response: %w", err)
	}
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	return &Response{Status: res.StatusCode, Body: data, ContentType: ct}, nil
}

// Connection is one account the gateway holds for a provider (an OAuth account,
// an API-key connection, or an always-on no-auth public provider). It is the
// subset of GET /v1/connections FloMorphic needs to hand a plugin so it can act
// as that account (the `Alias` selects it on later action calls).
type Connection struct {
	ID           string `json:"id"`
	Service      string `json:"service"`
	Status       string `json:"status"`
	AccountLabel string `json:"accountLabel"`
	Alias        string `json:"alias"`
	AuthType     string `json:"authType"`
	IsDefault    bool   `json:"isDefault"`
}

// Connections lists the connected accounts this token can use (GET
// /v1/connections), unwrapping the gateway's `{success,message,data}` envelope.
func (c *Client) Connections(ctx context.Context) ([]Connection, error) {
	res, err := c.Do(ctx, http.MethodGet, "/v1/connections", nil, nil)
	if err != nil {
		return nil, err
	}
	if res.Status >= 400 {
		return nil, fmt.Errorf("gateway returned %d for /v1/connections", res.Status)
	}
	var env struct {
		Data []Connection `json:"data"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, fmt.Errorf("openconnector: decode connections: %w", err)
	}
	return env.Data, nil
}

// Probe validates the base URL + token by hitting a lightweight authenticated
// endpoint (path). It returns nil when the gateway answers with a non-auth-error
// status and an error describing an unreachable gateway or a rejected token.
// Callers choose the path to match the token's scope: `/api/connections` for an
// admin token, `/v1/actions` for a runtime token.
func (c *Client) Probe(ctx context.Context, path string) error {
	res, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	switch {
	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden:
		return fmt.Errorf("gateway rejected the token (%d)", res.Status)
	case res.Status >= 500:
		return fmt.Errorf("gateway error (%d)", res.Status)
	case res.Status >= 400:
		return fmt.Errorf("gateway returned %d", res.Status)
	default:
		return nil
	}
}
