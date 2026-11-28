package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// HTTPEmbedderConfig configures an OpenAI-compatible embeddings client for
// document indexing and query vector generation without importing pkg/llm.
type HTTPEmbedderConfig struct {
	BaseURL        string
	APIKeyEnv      string
	Model          string
	Dimensions     int
	QueryPrefix    string
	DocumentPrefix string
	Timeout        time.Duration
	LookupEnv      func(string) string
	HTTPClient     *http.Client
}

// HTTPEmbedder calls an OpenAI-compatible /embeddings endpoint.
type HTTPEmbedder struct {
	endpoint       string
	apiKey         string
	model          string
	dimensions     int
	queryPrefix    string
	documentPrefix string
	client         *http.Client
}

// NewHTTPEmbedder validates cfg and returns a ready HTTPEmbedder.
func NewHTTPEmbedder(cfg HTTPEmbedderConfig) (*HTTPEmbedder, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("outbox: embedder base_url is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, errors.New("outbox: embedder model is required")
	}
	var apiKey string
	if envName := strings.TrimSpace(cfg.APIKeyEnv); envName != "" {
		lookup := cfg.LookupEnv
		if lookup == nil {
			lookup = os.Getenv
		}
		apiKey = strings.TrimSpace(lookup(envName))
		if apiKey == "" {
			return nil, fmt.Errorf("outbox: embedder environment variable %s is empty", envName)
		}
	}
	client := cfg.HTTPClient
	if client == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		client = &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &HTTPEmbedder{
		endpoint:       baseURL + "/embeddings",
		apiKey:         apiKey,
		model:          model,
		dimensions:     cfg.Dimensions,
		queryPrefix:    cfg.QueryPrefix,
		documentPrefix: cfg.DocumentPrefix,
		client:         client,
	}, nil
}

// EmbedDocument embeds a document string using DocumentPrefix.
func (e *HTTPEmbedder) EmbedDocument(ctx context.Context, text string) (model string, dim int, vector []float32, err error) {
	if e == nil {
		return "", 0, nil, errors.New("outbox: nil embedder")
	}
	vec, err := e.embed(ctx, e.documentPrefix+text)
	if err != nil {
		return "", 0, nil, err
	}
	return e.model, len(vec), vec, nil
}

// EmbedQuery embeds a search query string using QueryPrefix.
func (e *HTTPEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if e == nil {
		return nil, errors.New("outbox: nil embedder")
	}
	return e.embed(ctx, e.queryPrefix+text)
}

func (e *HTTPEmbedder) embed(ctx context.Context, input string) ([]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload := struct {
		Model      string `json:"model"`
		Input      string `json:"input"`
		Dimensions int    `json:"dimensions,omitempty"`
	}{
		Model:      e.model,
		Input:      input,
		Dimensions: e.dimensions,
	}
	rawReq, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("outbox: marshal embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(rawReq))
	if err != nil {
		return nil, fmt.Errorf("outbox: build embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("outbox: execute embedding request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("outbox: read embedding response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("outbox: embedding endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("outbox: decode embedding response: %w", err)
	}
	if len(decoded.Data) == 0 || len(decoded.Data[0].Embedding) == 0 {
		return nil, errors.New("outbox: embedding response contained no vectors")
	}
	return decoded.Data[0].Embedding, nil
}
