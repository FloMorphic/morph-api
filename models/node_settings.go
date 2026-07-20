package models

// NodeSetting is a reusable, named settings profile bound to a node. A node —
// identified by `NodeUniqID`, the node kind / plugin identity shared by every
// instance of that node — may own several profiles (e.g. a distinct URL + token
// per environment). A canvas node instance references one profile by id; the
// drawer lists a node's profiles so a configured one can be picked by name
// without re-entering its values.
//
// `Settings` is a free-form key/value object (the node's global settings from
// the inflowv1 plugin — access token, provider, endpoints, …). JSON tags mirror
// flomorphic-wapp's NodeSetting interface so the wire shape is drop-in.
type NodeSetting struct {
	ID         string `json:"id"`
	NodeUniqID string `json:"nodeUniqId"`
	// NodeType is the node's kind (e.g. "llm", "plugin", "http"). It is set by
	// the frontend from the node being edited — not entered by the user — and
	// records what kind of node the profile belongs to (while NodeUniqID
	// identifies which specific node/plugin).
	NodeType  string         `json:"nodeType"`
	Title     string         `json:"title"`
	Settings  map[string]any `json:"settings"`
	CreatedAt int64          `json:"createdAt"`
	UpdatedAt int64          `json:"updatedAt"`
}
