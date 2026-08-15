package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	llmclient "github.com/snowmerak/q/client"
	qembedding "github.com/snowmerak/q/embedding"
)

const (
	ServiceName         = "q-library"
	ProtocolVersion     = 1
	Implementation      = "0.1.0"
	registrationTimeout = 2 * time.Minute
)

type Health struct {
	Service         string `json:"service"`
	ProtocolVersion int    `json:"protocol_version"`
	Implementation  string `json:"implementation"`
	StoreID         string `json:"store_id"`
	Generation      string `json:"generation"`
	Ready           bool   `json:"ready"`
}

func (h Health) Compatible() bool {
	return h.Service == ServiceName && h.ProtocolVersion == ProtocolVersion && h.Ready
}

type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
	register *http.Client

	embeddingMu sync.RWMutex
	embedding   embeddingClientConfig
}

// Embedder is the subset of q's configured LLM client needed for Library
// proposition registration and semantic search.
type Embedder interface {
	Embed(context.Context, llmclient.EmbeddingRequest) (*llmclient.EmbeddingResponse, error)
}

type embeddingClientConfig struct {
	provider   Embedder
	model      string
	dimensions int
}

func NewClient(endpoint, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = time.Duration(defaultProbeTimeout) * time.Millisecond
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"), apiKey: apiKey,
		http: &http.Client{Timeout: timeout}, register: &http.Client{Timeout: max(timeout, registrationTimeout)},
	}
}

func (c *Client) Endpoint() string { return c.endpoint }

// ConfigureEmbedding enables automatic proposition embedding on this client.
// A zero model and dimensions clear the configuration and retain BM25-only
// behavior. The Library server still validates the configured graph shape.
func (c *Client) ConfigureEmbedding(provider Embedder, model string, dimensions int) error {
	if c == nil {
		return errors.New("library: client is nil")
	}
	model = strings.TrimSpace(model)
	if model == "" && dimensions == 0 {
		c.embeddingMu.Lock()
		c.embedding = embeddingClientConfig{}
		c.embeddingMu.Unlock()
		return nil
	}
	if provider == nil || model == "" || dimensions < 1 || dimensions > 4096 {
		return errors.New("library: embedding provider, model, and dimensions between 1 and 4096 are required together")
	}
	c.embeddingMu.Lock()
	c.embedding = embeddingClientConfig{provider: provider, model: model, dimensions: dimensions}
	c.embeddingMu.Unlock()
	return nil
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	return c.getHealth(ctx, "/health", false)
}

func (c *Client) Status(ctx context.Context) (Health, error) {
	return c.getHealth(ctx, "/status", true)
}

func (c *Client) getHealth(ctx context.Context, path string, authenticated bool) (Health, error) {
	if c == nil {
		return Health{}, errors.New("library: client is nil")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return Health{}, err
	}
	if authenticated && c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Health{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("library: %s returned HTTP %d", path, response.StatusCode)
	}
	var result Health
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Health{}, fmt.Errorf("library: decode %s: %w", path, err)
	}
	return result, nil
}

func (c *Client) SearchSkills(ctx context.Context, input SkillSearchRequest) (SkillSearchResponse, error) {
	var output SkillSearchResponse
	if err := c.doJSON(ctx, http.MethodPost, "/skills/search", input, &output); err != nil {
		return SkillSearchResponse{}, err
	}
	return output, nil
}

func (c *Client) GetSkill(ctx context.Context, id, path string) (SkillResource, error) {
	if strings.TrimSpace(path) == "" {
		path = "SKILL.md"
	}
	escapedPath := make([]string, 0)
	for _, part := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		escapedPath = append(escapedPath, url.PathEscape(part))
	}
	endpoint := "/skills/" + url.PathEscape(strings.TrimSpace(id)) + "/resources/" + strings.Join(escapedPath, "/")
	var output SkillResource
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &output); err != nil {
		return SkillResource{}, err
	}
	return output, nil
}

func (c *Client) ReloadSkills(ctx context.Context) (SkillReloadResponse, error) {
	var output SkillReloadResponse
	if err := c.doJSON(ctx, http.MethodPost, "/skills/reload", struct{}{}, &output); err != nil {
		return SkillReloadResponse{}, err
	}
	return output, nil
}

func (c *Client) SearchPropositions(ctx context.Context, input PropositionSearchRequest) (PropositionSearchResponse, error) {
	query := strings.TrimSpace(input.Query)
	if len([]rune(query)) > maximumPropositionQueryRunes {
		return PropositionSearchResponse{}, fmt.Errorf("library: proposition query exceeds %d characters", maximumPropositionQueryRunes)
	}
	if len(input.Embedding) == 0 && query != "" {
		configured := c.embeddingConfig()
		if configured.provider != nil {
			vectors, err := embedPropositionTexts(ctx, configured, []string{query})
			if err != nil {
				return PropositionSearchResponse{}, fmt.Errorf("library: embed proposition search query: %w", err)
			}
			input.Embedding = vectors[0]
		}
	}
	var output PropositionSearchResponse
	if err := c.doJSON(ctx, http.MethodPost, "/propositions/search", input, &output); err != nil {
		return PropositionSearchResponse{}, err
	}
	return output, nil
}

func (c *Client) GetProposition(ctx context.Context, id string) (Proposition, error) {
	var output Proposition
	path := "/propositions/" + url.PathEscape(strings.TrimSpace(id))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &output); err != nil {
		return Proposition{}, err
	}
	return output, nil
}

func (c *Client) RegisterProposition(ctx context.Context, idempotencyKey string, input PropositionRegisterRequest) (PropositionRegisterResponse, error) {
	if input.Embeddings == nil {
		configured := c.embeddingConfig()
		if configured.provider != nil {
			normalized, err := normalizePropositionRegisterRequest(input)
			if err != nil {
				return PropositionRegisterResponse{}, err
			}
			texts := make([]string, 0, len(normalized.Queries)+1)
			texts = append(texts, normalized.Content)
			texts = append(texts, normalized.Queries...)
			vectors, err := embedPropositionTexts(ctx, configured, texts)
			if err != nil {
				return PropositionRegisterResponse{}, fmt.Errorf("library: embed proposition registration: %w", err)
			}
			normalized.Embeddings = &PropositionEmbeddings{
				Model: configured.model, Dimensions: configured.dimensions, Vectors: vectors,
			}
			input = normalized
		}
	}
	var output PropositionRegisterResponse
	if err := c.doJSONWithClient(ctx, c.register, http.MethodPost, "/propositions", input, &output, map[string]string{
		"Idempotency-Key": idempotencyKey,
	}); err != nil {
		return PropositionRegisterResponse{}, err
	}
	return output, nil
}

func (c *Client) embeddingConfig() embeddingClientConfig {
	if c == nil {
		return embeddingClientConfig{}
	}
	c.embeddingMu.RLock()
	defer c.embeddingMu.RUnlock()
	return c.embedding
}

func embedPropositionTexts(ctx context.Context, configured embeddingClientConfig, texts []string) ([][]float32, error) {
	vectorizer, err := qembedding.New(configured.provider, configured.model, configured.dimensions)
	if err != nil {
		return nil, err
	}
	return vectorizer.Embed(ctx, texts)
}

func (c *Client) DeleteProposition(ctx context.Context, id string) (PropositionDeleteResponse, error) {
	var output PropositionDeleteResponse
	path := "/propositions/" + url.PathEscape(strings.TrimSpace(id))
	if err := c.doJSON(ctx, http.MethodDelete, path, nil, &output); err != nil {
		return PropositionDeleteResponse{}, err
	}
	return output, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	return c.doJSONWithHeaders(ctx, method, path, input, output, nil)
}

func (c *Client) doJSONWithHeaders(ctx context.Context, method, path string, input, output any, headers map[string]string) error {
	return c.doJSONWithClient(ctx, c.http, method, path, input, output, headers)
}

func (c *Client) doJSONWithClient(ctx context.Context, httpClient *http.Client, method, path string, input, output any, headers map[string]string) error {
	if c == nil {
		return errors.New("library: client is nil")
	}
	if httpClient == nil {
		return errors.New("library: HTTP client is unavailable")
	}
	var body *bytes.Reader
	if input == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("library: encode %s: %w", path, err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		if envelope.Error.Message != "" {
			return fmt.Errorf("library: %s returned HTTP %d: %s", path, response.StatusCode, envelope.Error.Message)
		}
		return fmt.Errorf("library: %s returned HTTP %d", path, response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("library: decode %s: %w", path, err)
	}
	return nil
}
