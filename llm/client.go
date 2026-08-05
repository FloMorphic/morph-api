// Package llm is a thin chat-completion facade over langchaingo.
//
// Backend services that need a language model — today the Human-in-the-Loop
// conversation bot (see api/hitl) — go through here. langchaingo owns the
// provider clients and the role-typed message model, so this package stays small:
// it maps a stored provider profile (the same fields the frontend's settings
// schema writes — see flomorphic-wapp src/lib/settingsSchemas.ts) onto a
// langchaingo model, converts our role/text turns into `llms.MessageContent`,
// and returns the reply text.
//
//	provider      openai | openrouter | openai-compatible | anthropic | gemini
//	model         the model id ("gpt-4o-mini", "anthropic/claude-3.5-sonnet", …)
//	access_token  the API key / bearer token
//	url           optional custom base URL (OpenAI-compatible endpoints)
//	temperature   sampling temperature
//	max_tokens    optional response cap (0 ⇒ provider default)
//
// Building on langchaingo keeps the door open for the extensions this bot will
// want next — tool/function calling, streaming, more providers — without another
// hand-rolled HTTP client per provider.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/openai"
)

// geminiOpenAIBaseURL is Google's OpenAI-compatible endpoint for Gemini. Driving
// Gemini through the OpenAI client this way keeps the whole provider set on two
// light clients (openai + anthropic) and avoids pulling in the Google Cloud SDK
// that langchaingo's native googleai provider requires.
const geminiOpenAIBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"

// Role names for Message, in the task-thread vocabulary. `human` is the thread's
// name for a user turn; it maps to langchaingo's Human role.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleHuman     = "human"
)

// anthropicDefaultMaxTokens is sent when an Anthropic profile leaves max_tokens
// unset — the Messages API requires the field.
const anthropicDefaultMaxTokens = 1024

// Message is one turn handed to the model. Content is plain text.
type Message struct {
	Role string
	Text string
}

// Config is the resolved provider profile a single Chat call runs against.
type Config struct {
	Provider    string
	Model       string
	Token       string
	BaseURL     string
	Temperature float64
	MaxTokens   int
	// HasTemperature records whether the profile set a temperature at all, so a
	// zero value ("0") is honoured rather than dropped as "unset".
	HasTemperature bool
}

// ConfigFromSettings reads a Config out of a NodeSetting.Settings map. Values
// may arrive as strings (the typed settings form stores everything as text) or
// as native JSON numbers, so every field is coerced defensively.
func ConfigFromSettings(m map[string]any) Config {
	cfg := Config{
		Provider: strings.TrimSpace(strings.ToLower(mapStr(m, "provider"))),
		Model:    strings.TrimSpace(mapStr(m, "model")),
		Token:    strings.TrimSpace(mapStr(m, "access_token")),
		BaseURL:  strings.TrimRight(strings.TrimSpace(mapStr(m, "url")), "/"),
	}
	if raw, ok := m["temperature"]; ok {
		if f, ok := toFloat(raw); ok {
			cfg.Temperature = f
			cfg.HasTemperature = true
		}
	}
	if raw, ok := m["max_tokens"]; ok {
		if f, ok := toFloat(raw); ok {
			cfg.MaxTokens = int(f)
		}
	}
	return cfg
}

// Validate reports the first thing that would stop a Chat call, so callers can
// fail with a clear message before building a model.
func (c Config) Validate() error {
	if c.Provider == "" {
		return fmt.Errorf("no LLM provider configured")
	}
	if c.Model == "" {
		return fmt.Errorf("no model configured for provider %q", c.Provider)
	}
	if c.Token == "" {
		return fmt.Errorf("no access token configured for provider %q", c.Provider)
	}
	return nil
}

// Chat sends the conversation to the configured provider and returns the
// assistant's reply text in one shot.
func Chat(ctx context.Context, cfg Config, msgs []Message) (string, error) {
	return ChatStream(ctx, cfg, msgs, nil)
}

// ChatStream is Chat with incremental delivery: when onChunk is non-nil it is
// called with each token/segment as the provider emits it (langchaingo's
// streaming callback), so a caller can push the reply to a UI as it forms. It
// still returns the full, trimmed reply once generation completes, so the caller
// can persist it. A nil onChunk runs an ordinary non-streaming request.
func ChatStream(ctx context.Context, cfg Config, msgs []Message, onChunk func(string)) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	model, err := newModel(cfg)
	if err != nil {
		return "", fmt.Errorf("init %s model: %w", cfg.Provider, err)
	}

	content := make([]llms.MessageContent, 0, len(msgs))
	for _, m := range msgs {
		content = append(content, llms.TextParts(chatRole(m.Role), m.Text))
	}

	opts := callOptions(cfg)
	if onChunk != nil {
		opts = append(opts, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				onChunk(string(chunk))
			}
			return nil
		}))
	}

	resp, err := model.GenerateContent(ctx, content, opts...)
	if err != nil {
		return "", fmt.Errorf("llm request failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("model returned no choices")
	}
	return strings.TrimSpace(resp.Choices[0].Content), nil
}

// newModel builds the langchaingo model for a provider. The OpenAI client backs
// the whole OpenAI family (openai / openrouter / openai-compatible); only the
// base URL differs.
func newModel(cfg Config) (llms.Model, error) {
	switch cfg.Provider {
	case "anthropic":
		opts := []anthropic.Option{anthropic.WithToken(cfg.Token), anthropic.WithModel(cfg.Model)}
		if cfg.BaseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(cfg.BaseURL))
		}
		return anthropic.New(opts...)
	default: // openai / openrouter / openai-compatible / gemini / custom
		return openai.New(
			openai.WithToken(cfg.Token),
			openai.WithModel(cfg.Model),
			openai.WithBaseURL(openAIBaseURL(cfg)),
		)
	}
}

// callOptions maps the profile's sampling knobs to langchaingo call options.
func callOptions(cfg Config) []llms.CallOption {
	opts := make([]llms.CallOption, 0, 2)
	if cfg.HasTemperature {
		opts = append(opts, llms.WithTemperature(cfg.Temperature))
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 && cfg.Provider == "anthropic" {
		maxTokens = anthropicDefaultMaxTokens
	}
	if maxTokens > 0 {
		opts = append(opts, llms.WithMaxTokens(maxTokens))
	}
	return opts
}

// chatRole maps our thread roles onto langchaingo's message types.
func chatRole(role string) llms.ChatMessageType {
	switch role {
	case RoleSystem:
		return llms.ChatMessageTypeSystem
	case RoleAssistant:
		return llms.ChatMessageTypeAI
	default: // human / user / anything else
		return llms.ChatMessageTypeHuman
	}
}

// openAIBaseURL resolves the base URL for the OpenAI-family providers: an
// explicit override wins, otherwise the provider's default.
func openAIBaseURL(cfg Config) string {
	if cfg.BaseURL != "" {
		return cfg.BaseURL
	}
	switch cfg.Provider {
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "gemini":
		return geminiOpenAIBaseURL
	default:
		return "" // let the OpenAI client use its own default
	}
}

func mapStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
