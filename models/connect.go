package models

// ConnectConnection is one configured OpenConnector endpoint — the central auth
// gateway FloMorphic reaches external SaaS providers through (oomol's hosted
// service at console.oomol.com, or a self-hosted OpenConnector instance running
// on-prem). FloMorphic is single-customer per instance, but several connections
// may coexist (e.g. a hosted oomol account alongside an on-prem gateway); one is
// marked default and used whenever a caller does not name a connection.
//
// Token is the OpenConnector bearer used on every proxied call — a runtime token
// (`oct_…`) for the hosted service, or an admin/runtime token for a self-hosted
// gateway. It is stored so plugin runs and the Connect page can reach the
// gateway without re-entering it. Read responses never echo the raw token: they
// carry TokenSet + TokenPreview instead (see Sanitized).
type ConnectConnection struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	BaseURL string `json:"baseUrl"`
	// Token is the runtime token (`oct_…`) — write-only, blanked on every read.
	// It authenticates the execution surface (`/v1`, `/mcp`).
	Token string `json:"token,omitempty"`
	// AdminToken authenticates the management surface (`/api`, `/docs`, console) —
	// providers catalog, connections, OAuth. It only exists for a self-hosted
	// gateway, where the operator sets OOMOL_CONNECT_ADMIN_TOKEN; the hosted
	// oomol service protects `/api` with the account session, so it stays empty
	// there. Write-only, blanked on read like Token.
	AdminToken string `json:"adminToken,omitempty"`
	// *Set / *Preview are read-only, derived by Sanitized so the UI can show
	// whether each token is stored (and its tail) without exposing it.
	TokenSet          bool   `json:"tokenSet"`
	TokenPreview      string `json:"tokenPreview,omitempty"`
	AdminTokenSet     bool   `json:"adminTokenSet"`
	AdminTokenPreview string `json:"adminTokenPreview,omitempty"`
	// Kind is a free-form label for the endpoint flavour ("hosted" | "selfhosted"),
	// set by the client for display only; the base URL is what actually matters.
	Kind      string `json:"kind"`
	IsDefault bool   `json:"isDefault"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Sanitized returns a copy safe to send to the client: raw tokens are stripped
// and replaced by *Set flags plus short masked previews of their tails.
func (c ConnectConnection) Sanitized() ConnectConnection {
	out := c
	out.TokenSet = c.Token != ""
	out.TokenPreview = MaskToken(c.Token)
	out.AdminTokenSet = c.AdminToken != ""
	out.AdminTokenPreview = MaskToken(c.AdminToken)
	out.Token = ""
	out.AdminToken = ""
	return out
}

// MaskToken renders a stored bearer as a short, safe-to-display preview (its
// last 4 chars), never the whole secret.
func MaskToken(t string) string {
	switch {
	case t == "":
		return ""
	case len(t) <= 4:
		return "…"
	default:
		return "…" + t[len(t)-4:]
	}
}
