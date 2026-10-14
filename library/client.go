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
	ServiceName           = "q-library"
	ProtocolVersion       = 6
	Implementation        = "0.1.0"
	defaultRequestTimeout = 5 * time.Second
	registrationTimeout   = 2 * time.Minute
	indexingTimeout       = 5 * time.Minute
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
	http     *http.Client
	register *http.Client
	index    *http.Client

	embeddingMu sync.RWMutex
	embedding   embeddingClientConfig
	reconcile   chan struct{}
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

// NewClient retains the API-key argument for source compatibility. Library
// servers are loopback-only and do not authenticate HTTP requests.
// Timeout bounds ordinary requests. Indexing requests allow five minutes and
// proposition registration at least two minutes, subject to the caller's context deadline.
func NewClient(endpoint, _ string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return &Client{
		endpoint:  strings.TrimRight(endpoint, "/"),
		http:      &http.Client{Timeout: timeout},
		register:  &http.Client{Timeout: max(timeout, registrationTimeout)},
		index:     &http.Client{Timeout: indexingTimeout},
		reconcile: make(chan struct{}, 1),
	}
}

func (c *Client) Endpoint() string { return c.endpoint }

// ConfigureEmbedding enables automatic semantic query and indexing support on
// this client. A zero model and dimensions clear the configuration and retain
// BM25-only behavior. The Library server still validates the configured graph
// shape.
func (c *Client) ConfigureEmbedding(provider Embedder, model string, dimensions int) error {
	if c == nil {
		return errors.New("library: client is nil")
	}
	model = strings.TrimSpace(model)
	if err := c.acquireReconciliation(context.Background()); err != nil {
		return err
	}
	defer func() { <-c.reconcile }()
	if model == "" && dimensions == 0 {
		c.embeddingMu.Lock()
		c.embedding.model, c.embedding.dimensions = "", 0
		if provider != nil {
			c.embedding.provider = provider
		}
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
	return c.getHealth(ctx, "/health")
}

func (c *Client) Status(ctx context.Context) (Health, error) {
	return c.getHealth(ctx, "/status")
}

func (c *Client) getHealth(ctx context.Context, path string) (Health, error) {
	if c == nil {
		return Health{}, errors.New("library: client is nil")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return Health{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Health{}, err
	}
	defer func() { _ = response.Body.Close() }()
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
	query := strings.TrimSpace(input.Query)
	if query != "" || len(input.Embedding) > 0 {
		configured, unlock, err := c.lockEmbedding(ctx)
		if err != nil {
			return SkillSearchResponse{}, err
		}
		defer unlock()
		if configured.provider != nil {
			if _, err := c.syncSkillEmbeddings(ctx, configured); err != nil {
				return SkillSearchResponse{}, fmt.Errorf("library: repair skill embeddings before search: %w", err)
			}
			configured = c.embeddingConfig()
			if configured.model == "" {
				input.Embedding, input.EmbeddingModel = nil, ""
			} else if len(input.Embedding) == 0 || input.EmbeddingModel != configured.model || len(input.Embedding) != configured.dimensions {
				if query == "" {
					return SkillSearchResponse{}, errors.New("library: outdated query vector requires query text for repair")
				}
				vectors, err := embedTexts(ctx, configured, []string{query})
				if err != nil {
					return SkillSearchResponse{}, fmt.Errorf("library: embed skill search query: %w", err)
				}
				input.Embedding = vectors[0]
				input.EmbeddingModel = configured.model
			}
		}
	}
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
	for part := range strings.SplitSeq(strings.ReplaceAll(path, "\\", "/"), "/") {
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
	if err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/skills/reload", struct{}{}, &output, nil); err != nil {
		return SkillReloadResponse{}, err
	}
	if c.embeddingConfig().provider != nil {
		if _, err := c.SyncSkillEmbeddings(ctx); err != nil {
			return SkillReloadResponse{}, err
		}
	}
	return output, nil
}

func (c *Client) SearchPropositions(ctx context.Context, input PropositionSearchRequest) (PropositionSearchResponse, error) {
	query := strings.TrimSpace(input.Query)
	if len([]rune(query)) > maximumPropositionQueryRunes {
		return PropositionSearchResponse{}, fmt.Errorf("library: proposition query exceeds %d characters", maximumPropositionQueryRunes)
	}
	if query != "" || len(input.Embedding) > 0 {
		configured, unlock, err := c.lockEmbedding(ctx)
		if err != nil {
			return PropositionSearchResponse{}, err
		}
		defer unlock()
		if configured.provider != nil {
			if _, err := c.syncEmbeddings(ctx, configured); err != nil {
				return PropositionSearchResponse{}, fmt.Errorf("library: repair embeddings before proposition search: %w", err)
			}
			configured = c.embeddingConfig()
			if configured.model == "" {
				input.Embedding = nil
			} else if len(input.Embedding) == 0 || input.EmbeddingModel != configured.model || len(input.Embedding) != configured.dimensions {
				if query == "" {
					return PropositionSearchResponse{}, errors.New("library: outdated query vector requires query text for repair")
				}
				vectors, err := embedTexts(ctx, configured, []string{query})
				if err != nil {
					return PropositionSearchResponse{}, fmt.Errorf("library: embed proposition search query: %w", err)
				}
				input.Embedding = vectors[0]
				input.EmbeddingModel = configured.model
			}
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
	configured, unlock, err := c.lockEmbedding(ctx)
	if err != nil {
		return PropositionRegisterResponse{}, err
	}
	defer unlock()
	if configured.provider != nil {
		if _, err := c.syncEmbeddings(ctx, configured); err != nil {
			return PropositionRegisterResponse{}, fmt.Errorf("library: repair embeddings before proposition registration: %w", err)
		}
		configured = c.embeddingConfig()
		if configured.model == "" {
			input.Embeddings = nil
		} else if input.Embeddings != nil && (input.Embeddings.Model != configured.model || input.Embeddings.Dimensions != configured.dimensions) {
			input.Embeddings = nil
		}
	}
	if input.Embeddings == nil {
		if configured.provider != nil && configured.model != "" {
			normalized, err := normalizePropositionRegisterRequest(input)
			if err != nil {
				return PropositionRegisterResponse{}, err
			}
			texts := make([]string, 0, len(normalized.Queries)+1)
			texts = append(texts, normalized.Content)
			texts = append(texts, normalized.Queries...)
			vectors, err := embedTexts(ctx, configured, texts)
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

// Serialize repair and semantic use, holding the selected model stable until
// the request finishes. Waiting callers can cancel and then retry on next use.
func (c *Client) lockEmbedding(ctx context.Context) (embeddingClientConfig, func(), error) {
	if err := c.acquireReconciliation(ctx); err != nil {
		return embeddingClientConfig{}, nil, err
	}
	return c.embeddingConfig(), func() { <-c.reconcile }, nil
}

func (c *Client) acquireReconciliation(ctx context.Context) error {
	if c == nil {
		return errors.New("library: client is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.reconcile <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func embedTexts(ctx context.Context, configured embeddingClientConfig, texts []string) ([][]float32, error) {
	vectorizer, err := qembedding.New(configured.provider, configured.model, configured.dimensions)
	if err != nil {
		return nil, err
	}
	return vectorizer.Embed(ctx, texts)
}

// EmbeddingSyncStats reports completed global record migrations.
type EmbeddingSyncStats struct {
	Skills       int
	Propositions int
}

// SyncEmbeddings migrates all global searchable records to the configured model.
// Repeating a failed migration fills only missing or outdated projections.
func (c *Client) SyncEmbeddings(ctx context.Context) (EmbeddingSyncStats, error) {
	configured, unlock, err := c.lockEmbedding(ctx)
	if err != nil {
		return EmbeddingSyncStats{}, err
	}
	defer unlock()
	return c.syncEmbeddings(ctx, configured)
}

func (c *Client) syncEmbeddings(ctx context.Context, configured embeddingClientConfig) (EmbeddingSyncStats, error) {
	skills, err := c.syncSkillEmbeddings(ctx, configured)
	configured = c.embeddingConfig()
	stats := EmbeddingSyncStats{Skills: skills.Embedded}
	if err != nil || configured.model == "" {
		return stats, err
	}
	propositions, err := c.syncEmbeddingSources(ctx, configured, "propositions")
	stats.Propositions = propositions.Embedded
	return stats, err
}

// SyncSkillEmbeddings configures the Library's rebuildable vector index and
// fills every active global skill that is missing field projections for this
// client's configured model. With no configured model it disables vectors and
// leaves skill search on BM25.
func (c *Client) SyncSkillEmbeddings(ctx context.Context) (SkillEmbeddingSyncStats, error) {
	configured, unlock, err := c.lockEmbedding(ctx)
	if err != nil {
		return SkillEmbeddingSyncStats{}, err
	}
	defer unlock()
	return c.syncSkillEmbeddings(ctx, configured)
}

func (c *Client) syncSkillEmbeddings(ctx context.Context, configured embeddingClientConfig) (SkillEmbeddingSyncStats, error) {
	vector := SkillVectorConfigRequest{Model: configured.model, Dimensions: configured.dimensions}
	var configuredResponse SkillVectorConfigResponse
	if err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/skills/embeddings/configure", vector, &configuredResponse, nil); err != nil {
		return SkillEmbeddingSyncStats{}, err
	}
	if configuredResponse.Vector != nil {
		configured.model, configured.dimensions = configuredResponse.Vector.Model, configuredResponse.Vector.Dimensions
		c.embeddingMu.Lock()
		c.embedding = configured
		c.embeddingMu.Unlock()
	}
	if configured.model == "" {
		return SkillEmbeddingSyncStats{}, nil
	}
	if configured.provider == nil {
		return SkillEmbeddingSyncStats{}, errors.New("library: active embedding model requires a provider")
	}
	return c.syncEmbeddingSources(ctx, configured, "skills")
}

func (c *Client) syncEmbeddingSources(ctx context.Context, configured embeddingClientConfig, kind string) (SkillEmbeddingSyncStats, error) {
	stats := SkillEmbeddingSyncStats{}
	for {
		var sources SkillEmbeddingSourceResponse
		if err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/"+kind+"/embeddings/sources", SkillEmbeddingSourceRequest{
			Model: configured.model, Dimensions: configured.dimensions, Limit: defaultSkillEmbedBatch,
		}, &sources, nil); err != nil {
			return stats, err
		}
		if len(sources.Sources) == 0 {
			if sources.Remaining != 0 {
				return stats, fmt.Errorf("library: %s embedding source scan made no progress", kind)
			}
			return stats, nil
		}
		texts := make([]string, 0, len(sources.Sources)*3)
		for _, source := range sources.Sources {
			for _, part := range source.Parts {
				texts = append(texts, part.Text)
			}
		}
		vectors := make([][]float32, 0, len(texts))
		for start := 0; start < len(texts); start += maximumSkillEmbedBatch {
			end := min(start+maximumSkillEmbedBatch, len(texts))
			chunk, err := embedTexts(ctx, configured, texts[start:end])
			if err != nil {
				return stats, fmt.Errorf("library: embed %s: %w", kind, err)
			}
			vectors = append(vectors, chunk...)
		}
		items := make([]SkillEmbeddingItem, len(sources.Sources))
		position := 0
		for index, source := range sources.Sources {
			items[index] = SkillEmbeddingItem{ID: source.ID, Digest: source.Digest}
			for _, part := range source.Parts {
				items[index].Projections = append(items[index].Projections, SkillEmbeddingProjection{
					ID: part.ID, Vector: vectors[position],
				})
				position++
			}
		}
		for start := 0; start < len(items); {
			batch, err := nextEmbeddingBatch(configured.model, configured.dimensions, items[start:])
			if err != nil {
				return stats, err
			}
			var applied SkillEmbeddingApplyResponse
			if err := c.doJSONWithClient(ctx, c.index, http.MethodPost, "/"+kind+"/embeddings", batch, &applied, nil); err != nil {
				return stats, err
			}
			if applied.Updated != len(batch.Items) {
				return stats, fmt.Errorf("library: applied %d of %d %s embeddings", applied.Updated, len(batch.Items), kind)
			}
			stats.Embedded += applied.Updated
			start += len(batch.Items)
		}
	}
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
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
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
