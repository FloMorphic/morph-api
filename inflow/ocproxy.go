package inflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/FloMorphic/morph-api/openconnector"
	"github.com/FloMorphic/morph-api/repository"
	natsHandler "github.com/Inflowenger/inflow-fusion/nats"
	InfraSpaces "github.com/Inflowenger/inflow-fusion/spaces"
	"github.com/bytedance/sonic"
	"github.com/nats-io/nats.go"
)

// The FloMorphic OpenConnector NATS proxy. It is the NATS twin of the HTTP
// `/connect/gateway/*` passthrough: a single, GENERIC forwarder that lets a
// plugin reach the OpenConnector gateway without holding a credential. It knows
// nothing about apps, accounts, actions or capabilities — it resolves the stored
// Connect connection, injects its token, forwards the request verbatim, and
// returns the gateway's raw response. All OpenConnector semantics (which app,
// which account, what an action requires) live in the plugin.
//
// It is served on the builtin-plugins NATS account (where plugins live) with an
// OPEN account credential, so any plugin ON THAT ACCOUNT can reach it. A plugin
// minted with a strict, plugin-scoped credential cannot publish on `flomorphic.>`.
// The subject lives OUTSIDE the inflow node protocol — inflow only provides the
// NATS transport.
const (
	OCService      = "flomorphic-oc-svc"
	OCProxySubject = "flomorphic.svc.oc.proxy"
)

// OCProxyRequest is what a plugin publishes: a gateway request to forward. `Path`
// is the OpenConnector path (e.g. "/v1/connections", "/v1/actions/gmail.send");
// `Connection` scopes to one Connect connection (default when empty); `Body` is
// forwarded verbatim.
type OCProxyRequest struct {
	Connection string            `json:"connection"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      map[string]string `json:"query"`
	Body       json.RawMessage   `json:"body"`
}

// OCProxyResponse is the reply: the gateway's HTTP status and raw JSON body, or
// an error when the request could not be made.
type OCProxyResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// StartOCServices subscribes the OpenConnector NATS proxy on the builtin-plugins
// account. Non-fatal for the rest of the backend: a failure here only means
// FloMorphic-coupled plugins cannot reach OpenConnector, logged by the caller.
func StartOCServices(store repository.Store) error {
	iso, err := InfraSpaces.GetCredOnBuiltinPluginAcc(OCService)
	if err != nil {
		return fmt.Errorf("oc-proxy: mint builtin-plugin credential: %w", err)
	}
	n, err := natsHandler.GetNatsByInfraIsolate(*iso)
	if err != nil {
		return fmt.Errorf("oc-proxy: connect NATS on plugin account: %w", err)
	}
	conn := n.GetConnection()
	if conn == nil {
		return fmt.Errorf("oc-proxy: nil NATS connection")
	}
	if _, err := conn.Subscribe(OCProxySubject, func(msg *nats.Msg) {
		respond(msg, runOCProxy(store, msg.Data))
	}); err != nil {
		return fmt.Errorf("oc-proxy: subscribe %s: %w", OCProxySubject, err)
	}
	fmt.Println("OC proxy registered on ", OCProxySubject)
	return nil
}

// respond marshals v and replies, falling back to an error envelope on failure.
func respond(msg *nats.Msg, v any) {
	b, err := sonic.Marshal(v)
	if err != nil {
		b = []byte(`{"error":"encode failed"}`)
	}
	_ = msg.Respond(b)
}

// runOCProxy forwards one request to the resolved Connect connection's gateway,
// verbatim, and returns the raw response. Generic: no OpenConnector semantics.
func runOCProxy(store repository.Store, body []byte) OCProxyResponse {
	var req OCProxyRequest
	if err := sonic.Unmarshal(body, &req); err != nil {
		return OCProxyResponse{Error: "invalid proxy request: " + err.Error()}
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		return OCProxyResponse{Error: "path is required"}
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "GET"
	}

	ctx := context.Background()
	conn, err := resolveConnection(store, req.Connection)
	if err != nil {
		return OCProxyResponse{Error: err.Error()}
	}
	token := tokenForOCPath(conn, path)
	if strings.TrimSpace(token) == "" {
		return OCProxyResponse{Error: "connection has no token for this path"}
	}

	query := url.Values{}
	for k, v := range req.Query {
		query.Set(k, v)
	}
	var reader *bytes.Reader
	if len(req.Body) > 0 {
		reader = bytes.NewReader(req.Body)
	} else {
		reader = bytes.NewReader(nil)
	}

	client := openconnector.New(conn.BaseURL, token)
	res, err := client.Do(ctx, method, path, query, reader)
	if err != nil {
		return OCProxyResponse{Error: err.Error()}
	}
	out := OCProxyResponse{Status: res.Status}
	if json.Valid(res.Body) {
		out.Body = json.RawMessage(res.Body)
	} else if len(res.Body) > 0 {
		b, _ := json.Marshal(string(res.Body))
		out.Body = b
	}
	return out
}

// tokenForOCPath mirrors the HTTP gateway proxy: the admin token authenticates
// the /api|/docs management surface (when stored), the runtime token everything
// else. Empty admin token falls back to the runtime token.
func tokenForOCPath(conn *models.ConnectConnection, path string) string {
	if (strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/docs")) && strings.TrimSpace(conn.AdminToken) != "" {
		return conn.AdminToken
	}
	return conn.Token
}

// resolveConnection returns the named Connect connection, or the default when the
// id is empty.
func resolveConnection(store repository.Store, id string) (*models.ConnectConnection, error) {
	if strings.TrimSpace(id) != "" {
		return store.Connect().GetByID(context.Background(), id)
	}
	return store.Connect().Default(context.Background())
}
