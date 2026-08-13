package models

// Trigger is what launches a workflow from the outside — an inbound webhook or a
// recurring schedule. It is a first-class record kept entirely apart from the
// flow graph (FlowRecord.ViewFlow): the graph is an editable, exportable document,
// so webhook secrets, the public URL, and delivery history must not live in it.
// A trigger only references the flow and the entry node it launches (FlowID +
// StartNodeID — StartNodeID empty resolves the flow's own start node, mirroring
// inflow.StartParams.StartNodeIDs). JSON tags mirror flomorphic-wapp's Trigger
// type. Backed by `/trigger`; the public ingress is `/hooks/:slug`.
//
// Note the split from the process scheduler: inflow.Scheduler fires a parked
// process ONCE at its ScheduledAt (the "Continue After" node). A schedule Trigger
// is recurring — when due it starts a fresh run and re-arms — so it is a distinct
// concept, driven by TriggerScheduler, not a scheduled process row.
type TriggerKind string

const (
	TriggerWebhook  TriggerKind = "webhook"
	TriggerSchedule TriggerKind = "schedule"
)

// WebhookAuthMethod is how an inbound webhook request is authenticated: no header
// (IP allow-list only), a static shared token, HTTP Basic, a verified JWT, or an
// HMAC body signature.
type WebhookAuthMethod string

const (
	AuthNone   WebhookAuthMethod = "none"
	AuthStatic WebhookAuthMethod = "static"
	AuthBasic  WebhookAuthMethod = "basic"
	AuthJWT    WebhookAuthMethod = "jwt"
	AuthHMAC   WebhookAuthMethod = "hmac"
)

// WebhookAuth is the credential check for a webhook. Secret is write-only: it is
// accepted on save and used server-side, but redacted before a Trigger is
// returned to a client (HasSecret reports whether one is stored).
type WebhookAuth struct {
	Method WebhookAuthMethod `json:"method"`
	// HeaderKey is the request header carrying the credential (e.g. Authorization,
	// X-Signature). Unused for `none`.
	HeaderKey string `json:"headerKey,omitempty"`
	// HeaderPattern is a regex whose last capture group extracts the token from
	// the header value (e.g. `^Bearer (.+)$`). Used by `jwt` / `hmac`.
	HeaderPattern string `json:"headerPattern,omitempty"`
	// HashAlgo is the HMAC hash: sha256 (default) / sha384 / sha512.
	HashAlgo string `json:"hashAlgo,omitempty"`
	// Digest is how the HMAC signature token is encoded: hex (default) / base64.
	Digest string `json:"digest,omitempty"`
	// Secret is the shared secret / token / HMAC key / JWT verification key.
	// Write-only — see the type doc.
	Secret string `json:"secret,omitempty"`
}

// TriggerHit is one recorded webhook delivery — the run log shown under the hook.
type TriggerHit struct {
	At      int64  `json:"at"`
	Status  int    `json:"status"`
	IP      string `json:"ip,omitempty"`
	Method  string `json:"method,omitempty"`
	Message string `json:"message,omitempty"`
}

// ScheduleMode is how a recurring schedule is expressed: a raw cron string or a
// plain interval. An interval is normalized to a cron-style spec on save
// (CronEffective) so the scheduler arms on a single representation.
type ScheduleMode string

const (
	ScheduleCron     ScheduleMode = "cron"
	ScheduleInterval ScheduleMode = "interval"
)

// TriggerContextMode is how a fire obtains the process's context document — the
// user's choice per trigger. `existing` reuses one selected doc (the run mutates
// it in place, like the manual Run dialog); `new` creates a fresh context each
// fire (isolated runs, at the cost of extra rows). A webhook's payload lands in
// whichever context the run uses.
type TriggerContextMode string

const (
	ContextExisting TriggerContextMode = "existing"
	ContextNew      TriggerContextMode = "new"
)

// RunSettings are the caller-tunable engine overrides carried on a trigger, the
// same three the manual Run dialog collects. A zero/absent field keeps the engine
// default, so a trigger only moves what the user set. Mapped to inflow.RunSettings
// at launch.
type RunSettings struct {
	ExecuteTimeoutSec int64  `json:"executeTimeoutSec,omitempty"`
	ProcessNodeLimit  uint16 `json:"processNodeLimit,omitempty"`
	RequestTimeoutSec int64  `json:"requestTimeoutSec,omitempty"`
}

// Trigger carries every kind's fields on one struct, discriminated by Kind. The
// webhook fields are meaningful when Kind == webhook, the schedule fields when
// Kind == schedule; the rest serialize empty (omitempty).
type Trigger struct {
	ID          string      `json:"id"`
	Kind        TriggerKind `json:"kind"`
	FlowID      string      `json:"flowId"`
	StartNodeID string      `json:"startNodeId"`
	Title       string      `json:"title"`
	Enabled     bool        `json:"enabled"`

	// ---- run context + settings (both kinds) ----
	// A fire starts a process, which needs a context document. ContextMode picks
	// where it comes from; ContextID names the doc for `existing`; ContextTitle is
	// the title of the doc minted for `new` (optional). Settings overrides the
	// engine run settings. Enabling a trigger requires a usable context (an
	// `existing` mode must name a doc).
	ContextMode  TriggerContextMode `json:"contextMode,omitempty"`
	ContextID    string             `json:"contextId,omitempty"`
	ContextTitle string             `json:"contextTitle,omitempty"`
	Settings     *RunSettings       `json:"settings,omitempty"`

	// ---- webhook ----
	Slug        string       `json:"slug,omitempty"`
	Methods     []string     `json:"methods,omitempty"`
	Auth        *WebhookAuth `json:"auth,omitempty"`
	WhitelistIP []string     `json:"whitelistIp,omitempty"`
	// URL is read-only: the fully-qualified public ingress URL.
	URL string `json:"url,omitempty"`
	// HasSecret is read-only: whether a secret is stored (the secret is redacted).
	HasSecret bool `json:"hasSecret,omitempty"`
	// RecentHits is read-only: recent deliveries, newest first (bounded).
	RecentHits []TriggerHit `json:"recentHits,omitempty"`

	// ---- schedule ----
	Mode        ScheduleMode `json:"mode,omitempty"`
	Cron        string       `json:"cron,omitempty"`
	IntervalSec int64        `json:"intervalSec,omitempty"`
	Timezone    string       `json:"timezone,omitempty"`
	// CronEffective is read-only: the single spec the scheduler actually arms on —
	// the given Cron, or the `@every` spec derived from IntervalSec.
	CronEffective string `json:"cronEffective,omitempty"`
	// NextAt is read-only: epoch millis of the next fire (computed on read).
	NextAt int64 `json:"nextAt,omitempty"`
	// LastAt is read-only: epoch millis of the last fire, 0 if never.
	LastAt int64 `json:"lastAt,omitempty"`

	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// MaxTriggerHits bounds the per-webhook delivery log kept on the record.
const MaxTriggerHits = 10

// Redact clears the write-only secret and sets HasSecret, so a Trigger is safe to
// return to a client. Call it on every read path.
func (t *Trigger) Redact() {
	if t.Auth != nil {
		t.HasSecret = t.Auth.Secret != ""
		t.Auth.Secret = ""
	}
}
