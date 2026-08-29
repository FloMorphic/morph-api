package svc

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// This file backs the "load available models" affordance on the web app's
// add-vector-store form: given a provider and its API key, it asks the provider
// which embedding models the key can use, so the form offers a real list instead
// of a free-text guess. Dimensions are not returned by any provider's list API,
// so the form keeps a numeric field the user confirms (some models support
// several output sizes).

// listModelsHTTPClient is separate from the embed client: listing is a cheap
// metadata GET, but it still wants a timeout so a slow provider cannot stall the
// request.
var listModelsHTTPClient = &http.Client{Timeout: 15 * time.Second}

// voyageEmbeddingModels is Voyage's published embedding line-up. Voyage has no
// list-models API, so the form falls back to this static set.
var voyageEmbeddingModels = []string{
	"voyage-3-large",
	"voyage-3",
	"voyage-3-lite",
	"voyage-code-3",
	"voyage-finance-2",
	"voyage-law-2",
}

// ListEmbeddingModels returns the embedding models a provider exposes to the
// given token. Each provider is queried on its own list-models endpoint and the
// result filtered to embedding models; Voyage (no list API) returns a static
// set. token is required for the providers that authenticate the listing call.
func ListEmbeddingModels(ctx context.Context, provider, token string) ([]string, error) {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return nil, fmt.Errorf("embed models: no provider given")
	}

	switch p {
	case "voyage", "voyageai":
		return voyageEmbeddingModels, nil
	case "gemini", "google":
		return listGeminiEmbeddingModels(ctx, token)
	case "cohere":
		return listCohereEmbeddingModels(ctx, token)
	default:
		// openai / mistral / together / a full OpenAI-compatible base URL: the
		// OpenAI `GET /models` list, filtered to embedding ids.
		return listOpenAICompatibleEmbeddingModels(ctx, provider, token)
	}
}

// listOpenAICompatibleEmbeddingModels reads the OpenAI `GET /models` list from
// the provider's base URL and keeps the ids that name an embedding model.
func listOpenAICompatibleEmbeddingModels(ctx context.Context, provider, token string) ([]string, error) {
	base, err := embedBaseURL(provider)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, base+"/models", bearer(token), &out); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if isEmbeddingModelID(m.ID) {
			models = append(models, m.ID)
		}
	}
	sort.Strings(models)
	return models, nil
}

// listGeminiEmbeddingModels reads Google's native models list and keeps the ones
// that support the embedContent method. Google's list authenticates with a `key`
// query param, not a bearer header, and prefixes ids with "models/".
func listGeminiEmbeddingModels(ctx context.Context, token string) ([]string, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("embed models: gemini listing needs an API key")
	}
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	url := "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000&key=" + strings.TrimSpace(token)
	if err := getJSON(ctx, url, nil, &out); err != nil {
		return nil, err
	}
	models := make([]string, 0)
	for _, m := range out.Models {
		if !containsFold(m.SupportedGenerationMethods, "embedContent") {
			continue
		}
		models = append(models, strings.TrimPrefix(m.Name, "models/"))
	}
	sort.Strings(models)
	return models, nil
}

// listCohereEmbeddingModels reads Cohere's models list filtered to the embed
// endpoint — the one provider that filters embedding models server-side.
func listCohereEmbeddingModels(ctx context.Context, token string) ([]string, error) {
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, "https://api.cohere.ai/v1/models?endpoint=embed&page_size=1000", bearer(token), &out); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		if strings.TrimSpace(m.Name) != "" {
			models = append(models, m.Name)
		}
	}
	sort.Strings(models)
	return models, nil
}

// getJSON performs a GET and decodes a JSON body into v, surfacing a non-2xx
// status as an error so a bad key or endpoint fails visibly.
func getJSON(ctx context.Context, url string, header http.Header, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("embed models: build request: %w", err)
	}
	for k, vals := range header {
		for _, val := range vals {
			req.Header.Add(k, val)
		}
	}
	resp, err := listModelsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("embed models: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("embed models: provider returned status %d", resp.StatusCode)
	}
	if err := sonic.ConfigDefault.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("embed models: decode response: %w", err)
	}
	return nil
}

// bearer builds an Authorization header for a token, or nil when there is none.
func bearer(token string) http.Header {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return http.Header{"Authorization": []string{"Bearer " + strings.TrimSpace(token)}}
}

// isEmbeddingModelID reports whether an OpenAI-style model id names an embedding
// model. The providers on this path (OpenAI, Mistral, Together, …) all spell it
// with "embed" in the id (text-embedding-3-small, mistral-embed, …).
func isEmbeddingModelID(id string) bool {
	return strings.Contains(strings.ToLower(id), "embed")
}

// containsFold reports whether xs contains s, case-insensitively.
func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
