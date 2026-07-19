package models

// PromptVariable describes a placeholder a prompt template expects. The template
// text references it as `{{name}}`; the UI collects a value per variable.
type PromptVariable struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptRecord is a reusable prompt template. `Template` is the prompt text with
// `{{variable}}` placeholders; `Variables` documents those placeholders. Tags
// are free-form labels for grouping/search in the UI.
type PromptRecord struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Template    string           `json:"template"`
	Variables   []PromptVariable `json:"variables"`
	Tags        []string         `json:"tags"`
	CreatedAt   int64            `json:"createdAt"`
	UpdatedAt   int64            `json:"updatedAt"`
}
