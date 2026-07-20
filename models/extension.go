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

	// Builtin generics (mirror the palette's static generics tab / the
	// compiler's node kinds in inflow/compiler.go).
	ExtStartType        ExtensionType = "startNode"
	ExtPluginNativeType ExtensionType = "pluginNative"
	ExtCodeType         ExtensionType = "code"
	ExtContractType     ExtensionType = "contract"
	ExtGotoType         ExtensionType = "goto"
	ExtVoidType         ExtensionType = "void"
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
