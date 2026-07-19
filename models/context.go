package models

// ContextChangeType records who last mutated a context: a running flow, or a
// manual edit through the API/panel.
type ContextChangeType string

const (
	ByFlow ContextChangeType = "flow"
	ByAPI  ContextChangeType = "manual"
)

// ContextRecord is a named context document referenced by workflows at runtime.
// `Context` is a JSON object serialized as a string (validated on write);
// `Header` holds arbitrary metadata. JSON tags mirror flomorphic-wapp's
// ContextRecord interface.
type ContextRecord struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Context   string         `json:"context"`
	Header    map[string]any `json:"header"`
	UpdatedBy LastChange     `json:"updatedBy"`
	CreatedAt int64          `json:"createdAt"`
	UpdatedAt int64          `json:"updatedAt"`
}

// LastChange is the `updatedBy` sub-object.
type LastChange struct {
	By      ContextChangeType `json:"by"`
	Address string            `json:"address"`
}
