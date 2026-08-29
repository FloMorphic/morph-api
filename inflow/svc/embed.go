package svc

import (
	"context"
	"fmt"
	"strings"

	"github.com/FloMorphic/morph-api/models"
	"github.com/tmc/langchaingo/llms/openai"
)

// Embedding providers all speak the OpenAI embeddings wire format, either
// natively (OpenAI) or through a documented OpenAI-compatibility endpoint
// (Gemini, Cohere, Mistral, Voyage, Together). Driving every provider through
// langchaingo's openai client — the same client llm/client.go uses for chat —
// keeps the backend on one embedding path instead of a hand-rolled HTTP client
// per provider, and lets Gemini reuse the OpenAI-compat base URL trick already
// established for chat (see llm.geminiOpenAIBaseURL).
//
// providerBaseURLs maps a known provider name to the base of its
// OpenAI-compatible embeddings API, including the `/v1`-style path segment
// langchaingo appends `/embeddings` onto. A store may instead set Provider to a
// full base URL (see embedBaseURL) for a self-hosted or otherwise
// OpenAI-compatible endpoint.
var providerBaseURLs = map[string]string{
	"openai":   "https://api.openai.com/v1",
	"gemini":   "https://generativelanguage.googleapis.com/v1beta/openai",
	"google":   "https://generativelanguage.googleapis.com/v1beta/openai",
	"cohere":   "https://api.cohere.ai/compatibility/v1",
	"mistral":  "https://api.mistral.ai/v1",
	"voyage":   "https://api.voyageai.com/v1",
	"voyageai": "https://api.voyageai.com/v1",
	"together": "https://api.together.xyz/v1",
}

// dimensionCapableModels is the set of embedding models that honour the OpenAI
// `dimensions` request field (Matryoshka / configurable-output models). Only for
// these do we forward the store's configured Dimensions to the provider — sending
// the field to a fixed-width model makes the provider reject the request. Keyed by
// lower-cased model id. A model absent here is assumed fixed-width: the provider
// returns its native dimension and embedTexts validates it matches the store.
var dimensionCapableModels = map[string]bool{
	"text-embedding-3-small": true,
	"text-embedding-3-large": true,
	"gemini-embedding-001":   true,
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

	embedder, err := newEmbedder(cfg)
	if err != nil {
		return nil, err
	}

	vectors, err := embedder.CreateEmbedding(ctx, inputs)
	if err != nil {
		return nil, fmt.Errorf("embed: call %s: %w", cfg.Provider, err)
	}
	if len(vectors) != len(inputs) {
		return nil, fmt.Errorf("embed: expected %d vectors, got %d", len(inputs), len(vectors))
	}
	for _, v := range vectors {
		if len(v) != cfg.Dimensions {
			return nil, fmt.Errorf("embed: model %q returned %d dimensions but the store index expects %d",
				cfg.EmbeddingModel, len(v), cfg.Dimensions)
		}
	}
	return vectors, nil
}

// newEmbedder builds the langchaingo openai client for a store's embedding
// config: the provider's OpenAI-compatible base URL, the store token as the
// bearer key, and the embedding model. The configured Dimensions is forwarded
// only for models that accept it (see dimensionCapableModels) so a fixed-width
// model is not handed a `dimensions` field it would reject.
func newEmbedder(cfg *models.VectorMemoryConfig) (*openai.LLM, error) {
	base, err := embedBaseURL(cfg.Provider)
	if err != nil {
		return nil, err
	}

	opts := []openai.Option{
		openai.WithToken(cfg.Token),
		openai.WithBaseURL(base),
		openai.WithEmbeddingModel(cfg.EmbeddingModel),
	}
	if cfg.Dimensions > 0 && dimensionCapableModels[strings.ToLower(strings.TrimSpace(cfg.EmbeddingModel))] {
		opts = append(opts, openai.WithEmbeddingDimensions(cfg.Dimensions))
	}

	llm, err := openai.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("embed: init %s embedder: %w", cfg.Provider, err)
	}
	return llm, nil
}

// EmbedOne is the exported single-text embedding entry point for callers outside
// this package (e.g. the memory REST controller, which embeds a search query or
// a document to index using the store's captured provider/model/token). It is a
// thin wrapper over the internal embedOne.
func EmbedOne(ctx context.Context, cfg *models.VectorMemoryConfig, input string) ([]float32, error) {
	return embedOne(ctx, cfg, input)
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
// a known provider name maps to its OpenAI-compatible endpoint, and a value that
// is already a URL is used as-is (with any trailing slash trimmed) so a
// self-hosted or otherwise OpenAI-compatible endpoint works without code changes.
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
