package svc

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/FloMorphic/morph-api/models"
	"github.com/bytedance/sonic"
)

// providerBaseURLs maps a known embedding provider name to the base URL of its
// OpenAI-compatible embeddings API. A store may instead set Provider to a full
// URL (see embedBaseURL), so this only needs the common shorthands.
var providerBaseURLs = map[string]string{
	"openai":   "https://api.openai.com/v1",
	"voyage":   "https://api.voyageai.com/v1",
	"voyageai": "https://api.voyageai.com/v1",
	"mistral":  "https://api.mistral.ai/v1",
	"together": "https://api.together.xyz/v1",
}

// embedHTTPClient is shared; embedding calls are short and the timeout guards a
// hung provider from stalling a svc request past its ReqTimeoutSecound.
var embedHTTPClient = &http.Client{Timeout: 20 * time.Second}

// embedRequest / embedResponse mirror the OpenAI embeddings API, which the
// supported providers all speak. `input` accepts a batch so index/search can
// embed several texts in one round trip.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// embedTexts turns each input string into a vector using the store's captured
// provider/model/token. It returns one vector per input in the same order, each
// validated to be exactly cfg.Dimensions wide so a misconfigured store fails
// loudly rather than writing a vector the index will reject. inputs must be
// non-empty; empty strings are rejected (a provider would either error or return
// a meaningless zero vector).
func embedTexts(ctx context.Context, cfg *models.VectorMemoryConfig, inputs []string) ([][]float32, error) {
	if cfg == nil {
		return nil, fmt.Errorf("embed: vector config is nil")
	}
	if strings.TrimSpace(cfg.EmbeddingModel) == "" {
		return nil, fmt.Errorf("embed: store has no embeddingModel configured")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("embed: store has no provider token configured")
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("embed: no input text to embed")
	}
	for i, in := range inputs {
		if strings.TrimSpace(in) == "" {
			return nil, fmt.Errorf("embed: input %d is empty", i)
		}
	}

	base, err := embedBaseURL(cfg.Provider)
	if err != nil {
		return nil, err
	}

	payload, err := sonic.Marshal(embedRequest{Model: cfg.EmbeddingModel, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := embedHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: call %s: %w", cfg.Provider, err)
	}
	defer resp.Body.Close()

	var out embedResponse
	if err := sonic.ConfigDefault.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := "provider returned an error"
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return nil, fmt.Errorf("embed: %s status %d: %s", cfg.Provider, resp.StatusCode, msg)
	}
	if len(out.Data) != len(inputs) {
		return nil, fmt.Errorf("embed: expected %d vectors, got %d", len(inputs), len(out.Data))
	}

	// The API does not guarantee ordering, so index by the response's `index`.
	vectors := make([][]float32, len(inputs))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vectors) {
			return nil, fmt.Errorf("embed: response index %d out of range", d.Index)
		}
		if len(d.Embedding) != cfg.Dimensions {
			return nil, fmt.Errorf("embed: model %q returned %d dimensions but the store index expects %d",
				cfg.EmbeddingModel, len(d.Embedding), cfg.Dimensions)
		}
		vectors[d.Index] = d.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("embed: no vector returned for input %d", i)
		}
	}
	return vectors, nil
}

// embedOne is the single-text convenience over embedTexts.
func embedOne(ctx context.Context, cfg *models.VectorMemoryConfig, input string) ([]float32, error) {
	vecs, err := embedTexts(ctx, cfg, []string{input})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// embedBaseURL resolves the embeddings API base URL from the store's provider:
// a known provider name maps to its endpoint, and a value that is already a URL
// is used as-is (with any trailing slash trimmed) so a self-hosted or otherwise
// OpenAI-compatible endpoint works without code changes.
func embedBaseURL(provider string) (string, error) {
	p := strings.TrimSpace(provider)
	if p == "" {
		return "", fmt.Errorf("embed: store has no provider configured")
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return strings.TrimRight(p, "/"), nil
	}
	if base, ok := providerBaseURLs[strings.ToLower(p)]; ok {
		return base, nil
	}
	return "", fmt.Errorf("embed: unknown provider %q (use a known provider name or a full base URL)", provider)
}
