package models

// ExtensionKind is the *origin* of a palette node: whether it ships with
// FloMorphic and is managed by an admin (builtin), or was imported at runtime by
// a user as a third-party inflowv1 plugin (extension). Both kinds live in the
// same table and together make up the node palette.
type ExtensionKind string

const (
	// KindBuiltin nodes ship with the release and are seeded on first run
	// (see the builtins seed). Their settings/actions UI is hard-coded in the
	// front end; they do NOT speak the inflowv1 plugin protocol.
	KindBuiltin ExtensionKind = "builtin"
	// KindExtension nodes are added by users through the Extension portal by
	// importing a plugin. Their settings form and actions are driven live over
	// inflowv1 (keyed by PluginID); nothing about them is hard-coded.
	KindExtension ExtensionKind = "extension"
)

// ExtensionType is the *generic node type* an extension compiles to — the inflow
// generic (and front-end palette) type. Extrinsic and plugin are the two ways a
// node reaches the outside world; the rest are the builtin generics mirrored
// from the palette's "generics" tab.
type ExtensionType string

const (
	// The two generic execution types a user plugin binds as.
	ExtEventBaseType  ExtensionType = "extrinsic"
	ExtPluginBaseType ExtensionType = "plugin"

	// Builtin morphic types — FloMorphic's 10 palette nodes. Each is lowered to
	// an inflow primitive at compile time (see inflow/compiler.go's NodeBuilder).
	ExtStartType    ExtensionType = "startNode"  // -> void (start marker)
	ExtHitlType     ExtensionType = "hitl"       // -> extrinsic (svc.hitl.add)
	ExtDocStoreType ExtensionType = "docstore"   // -> extrinsic (svc.store.doc.{ACTION})
	ExtVecStoreType ExtensionType = "vecstore"   // -> extrinsic (svc.store.vec.{ACTION})
	ExtPromiseAll   ExtensionType = "promissall" // -> void (depends on all inbound)
	ExtLLMType      ExtensionType = "llm"        // -> plugin
	ExtMCPType      ExtensionType = "mcp"        // -> plugin (MCP client)
	ExtRuleType     ExtensionType = "rule"       // -> contract
	ExtJSType       ExtensionType = "js"         // -> code (variant js)
	ExtOPAType      ExtensionType = "opa"        // -> code (variant opa)
	ExtGotoType     ExtensionType = "goto"       // -> goto
	ExtUntilType    ExtensionType = "until"      // -> extrinsic (svc.continue.at)
	ExtCastType     ExtensionType = "cast"       // -> plugin
	ExtHTTPType     ExtensionType = "http"       // -> plugin (HTTP / REST client)
)

// ExtensionRecord is one palette node: metadata plus, for extrinsic nodes, the
// topic/values it binds to when compiled (see the inflow compiler's extension
// branch). For KindExtension nodes PluginID identifies the inflowv1 plugin whose
// settings form and actions are fetched live (never stored here).
type ExtensionRecord struct {
	ID          string        `json:"id"`
	Kind        ExtensionKind `json:"kind"`
	Type        ExtensionType `json:"type"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	// PluginID is the inflowv1 PLUGIN_ID for KindExtension nodes — the id used
	// to build `inflow.v1.<PLUGIN_ID>.…` subjects when fetching the plugin's
	// intro/settings/actions/forms live. Empty for builtins.
	PluginID   string         `json:"pluginId"`
	Icon       Icon           `json:"icon"`
	Parameters FormParameters `json:"params"`
	BindTo     Bind           `json:"bindTo"`
	// Install describes how the user runs the plugin behind this row. It is
	// documentation the install endpoints render into a script / env file — the
	// backend never clones or executes anything itself.
	Install InstallSpec `json:"install"`
	// Action names the one inflowv1 method this palette entry runs, for the rows
	// derived from a plugin's `@actions` (a Jira plugin contributes an "add
	// task" node, an "update task" node, …). Empty on a plugin's own
	// registration row and on builtins. It compiles to the plugin node's
	// `request`, and it is what separates a derived row — replaced wholesale on
	// every sync — from a user-owned one.
	Action string `json:"action"`
	// ParentID links a derived action row back to the registration row it was
	// synced from, so the portal can group them and a delete can take them with
	// it. Empty on non-derived rows.
	ParentID  string `json:"parentId"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// --- inflowv1 descriptors (read live, never stored) -------------------------
//
// These mirror the plugin SDK's wire shapes (sdkv1/models.go). The backend only
// passes them through — a plugin is the sole authority on what it exposes — but
// the sync pass has to read `@actions` to turn each action into a palette row.

// PluginIntro is `inflow.v1.<pluginId>.@intro`: who the plugin is, plus the
// settings form it needs filled in before any action runs. That form is the
// plugin's onboarding — the web app renders it into a settings profile.
type PluginIntro struct {
	Name     string       `json:"name"`
	Author   string       `json:"author"`
	Version  string       `json:"version"`
	Settings *FormBuilder `json:"settings,omitempty"`
}

// PluginAction is one entry of `inflow.v1.<pluginId>.@actions`: a method the
// plugin can run, with the label/icon a palette needs and the form its
// parameters are collected through.
type PluginAction struct {
	Method      string      `json:"method"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Icon        PluginIcon  `json:"icon"`
	Form        FormBuilder `json:"form"`
}

// PluginIcon is the SDK's icon reference for an action.
type PluginIcon struct {
	Ref  string `json:"ref"`
	Icon string `json:"icon"`
}

// FormBuilder is the SDK's form descriptor. `Jsonschema` / `Jsonui` are JSON
// *documents carried as strings* — that is the SDK's wire format, and they are
// stored and forwarded exactly as received.
type FormBuilder struct {
	SubmitTo   string `json:"submit_to"`
	Jsonui     string `json:"jsonui"`
	Jsonschema string `json:"jsonschema"`
}

// SyncResult reports what a sync did to a plugin's palette rows.
type SyncResult struct {
	Intro    PluginIntro    `json:"intro"`
	Actions  []PluginAction `json:"actions"`
	Added    int            `json:"added"`
	Removed  int            `json:"removed"`
	PluginID string         `json:"pluginId"`
}

// EnvVar is one extra environment entry a plugin needs beyond the three the
// inflowv1 SDK requires (PLUGIN_ID / INFRA_URL / INFRA_CRED) — an upstream API
// key, an endpoint, a mode flag. Values are stored as given and rendered into
// the generated env file, so treat the store as secret-bearing.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// InstallSpec is the "how do I run this plugin" half of an extension row: where
// its source lives and what env it needs. Everything is optional — a plugin the
// user already has on disk only needs the env half, which is exactly the second
// onboarding path (register the id here, copy the env file).
type InstallSpec struct {
	// Repo is the git remote to clone (https or ssh). Empty for a plugin the
	// user obtained themselves.
	Repo string `json:"repo"`
	// Ref is the branch / tag / commit to check out. Defaults to the remote head.
	Ref string `json:"ref"`
	// Subdir is the path inside the repo holding the plugin module, for repos
	// that ship several plugins (e.g. "llm" in builtin-plugins).
	Subdir string `json:"subdir"`
	// Runtime picks how the generated script builds and starts the plugin:
	// "auto" (detect from the checkout), "go", "node" or "docker".
	Runtime InstallRuntime `json:"runtime"`
	// EnvFile is the dotenv filename the plugin reads (the Go SDK's convention
	// is `.env.inflow`; the shipped builtins use `.env.morph`).
	EnvFile string `json:"envFile"`
	// Env are the plugin-specific variables written into that file alongside the
	// minted credential.
	Env []EnvVar `json:"env"`
}

// InstallRuntime is how the generated installer builds and starts a plugin.
type InstallRuntime string

const (
	RuntimeAuto   InstallRuntime = "auto"
	RuntimeGo     InstallRuntime = "go"
	RuntimeNode   InstallRuntime = "node"
	RuntimeDocker InstallRuntime = "docker"
)

// InstallInfo is the answer to "how do I get this plugin running" for one
// registered extension: a one-liner to paste into a shell, the env file that
// one-liner writes, and the script URL it pulls. Both carry a freshly minted
// plugin credential, so the payload is secret-bearing.
type InstallInfo struct {
	// Command is the one-liner: `curl -fsSL <scriptUrl> | bash -s -- <dir>`.
	Command string `json:"command"`
	// ScriptURL is the raw installer endpoint the command pipes into bash.
	ScriptURL string `json:"scriptUrl"`
	// Script is that installer's body, so the UI can show what will run.
	Script string `json:"script"`
	// Env is the dotenv content the script writes (also the whole deliverable
	// for a user who already has the plugin checked out).
	Env string `json:"env"`
	// EnvFile is the filename Env should be saved as.
	EnvFile string `json:"envFile"`
	// Dir is the install directory the command targets.
	Dir string `json:"dir"`
	// PluginID is the inflowv1 identity the credential is scoped to.
	PluginID string `json:"pluginId"`
}

type Bind struct {
	TopicKey string            `json:"topic_key"`
	Values   map[string]string `json:"values"`
}

type Icon struct {
	Class string         `json:"class"`
	Name  string         `json:"name"`
	Meta  map[string]any `json:"meta"`
}

type FormParameters struct {
	Schema map[string]any `json:"schema"`
	UI     map[string]any `json:"ui"`
}

// AccessCredType selects how broad the minted plugin credential is: MultiPluginAccess
// grants an open credential on the account, StrictAccess scopes it to a single
// plugin's inflowv1 subjects (see the inflow-fusion PluginCredential* permissions).
type AccessCredType string

const (
	MultiPluginAccess AccessCredType = "multi"
	StrictAccess      AccessCredType = "strict"
)

// CredRequest asks the backend to mint a runtime credential for a plugin-backed
// node (the builtin llm/mcp/cast nodes carry a hard-coded PluginID from the
// seed). The credential lets the plugin be run so it can serve the node's
// functionality. SpaceId is optional — empty means the builtin-plugins account.
type CredRequest struct {
	PluginId string         `json:"pluginId"`
	Name     string         `json:"name"`
	Access   AccessCredType `json:"access"`
	SpaceId  string         `json:"spaceId"`
}
