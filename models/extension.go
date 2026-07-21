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
)

// ExtensionRecord is one palette node: metadata plus, for extrinsic nodes, the
// topic/values it binds to when compiled (see the inflow compiler's extension
// branch). For KindExtension nodes PluginID identifies the inflowv1 plugin whose
// settings form and actions are fetched live (never stored here).
type ExtensionRecord struct {
	ID          string         `json:"id"`
	Kind        ExtensionKind  `json:"kind"`
	Type        ExtensionType  `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	// PluginID is the inflowv1 PLUGIN_ID for KindExtension nodes — the id used
	// to build `inflow.v1.<PLUGIN_ID>.…` subjects when fetching the plugin's
	// intro/settings/actions/forms live. Empty for builtins.
	PluginID   string         `json:"pluginId"`
	Icon       Icon           `json:"icon"`
	Parameters FormParameters `json:"params"`
	BindTo     Bind           `json:"bindTo"`
	CreatedAt  int64          `json:"createdAt"`
	UpdatedAt  int64          `json:"updatedAt"`
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
