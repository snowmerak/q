// Package opensearchindex implements sessionstore.SearchIndex backed by
// OpenSearch over HTTP/JSON without external client dependencies.
package opensearchindex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

const (
	MappingVersion      = "opensearch-v1"
	defaultIndexPrefix  = "q"
	defaultRefreshMode  = "wait_for"
	maximumIndexNameLen = 240
)

// Config configures an OpenSearch REST client shared across one or more
// derived indexes.
type Config struct {
	Endpoint    string
	Username    string
	Password    string
	BearerToken string
	IndexPrefix string
	Refresh     string
	HTTPClient  *http.Client
}

// Client is a reusable OpenSearch HTTP client safe for concurrent use by
// multiple Index adapters. Call Close when the owning service shuts down.
type Client struct {
	baseURL     string
	username    string
	password    string
	bearerToken string
	prefix      string
	refresh     string
	http        *http.Client
}

var _ io.Closer = (*Client)(nil)

// HTTPError reports a non-2xx HTTP status returned by OpenSearch.
type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("opensearchindex: %s %s returned HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func isNotFound(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

// NewClient validates cfg and builds a shared OpenSearch HTTP Client.
func NewClient(cfg Config) (*Client, error) {
	rawEndpoint := strings.TrimSpace(cfg.Endpoint)
	if rawEndpoint == "" {
		return nil, errors.New("opensearchindex: endpoint is required")
	}
	parsed, err := url.Parse(rawEndpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("opensearchindex: invalid HTTP(S) endpoint %q", cfg.Endpoint)
	}
	prefix := sanitizeIdentifier(cfg.IndexPrefix)
	if strings.TrimSpace(cfg.IndexPrefix) == "" {
		prefix = defaultIndexPrefix
	}
	if prefix == "" {
		return nil, fmt.Errorf("opensearchindex: invalid index prefix %q", cfg.IndexPrefix)
	}
	refresh := strings.TrimSpace(cfg.Refresh)
	if refresh == "" {
		refresh = defaultRefreshMode
	}
	if refresh != "wait_for" && refresh != "true" && refresh != "false" {
		return nil, fmt.Errorf("opensearchindex: unsupported refresh policy %q", cfg.Refresh)
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:     strings.TrimRight(rawEndpoint, "/"),
		username:    cfg.Username,
		password:    cfg.Password,
		bearerToken: cfg.BearerToken,
		prefix:      prefix,
		refresh:     refresh,
		http:        httpClient,
	}, nil
}

// Close closes idle connections on the underlying HTTP client.
func (c *Client) Close() error {
	if c == nil || c.http == nil {
		return nil
	}
	c.http.CloseIdleConnections()
	return nil
}

// Ping checks connectivity to the configured OpenSearch cluster.
func (c *Client) Ping(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/", nil, nil)
}

// IndexName returns the sanitized, bounded OpenSearch alias/index name for a
// logical index name.
func (c *Client) IndexName(logicalName string) string {
	clean := sanitizeIdentifier(logicalName)
	if clean == "" {
		clean = "index"
	}
	full := c.prefix + "-" + clean
	if len(full) <= maximumIndexNameLen {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "-" + hex.EncodeToString(sum[:8])
	return full[:maximumIndexNameLen-len(suffix)] + suffix
}

func sanitizeIdentifier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	lastDash := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			b.WriteByte(ch)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return ""
	}
	if out[0] < 'a' || out[0] > 'z' {
		if out[0] < '0' || out[0] > '9' {
			out = "q" + out
		}
	}
	return out
}

func (c *Client) newRequest(ctx context.Context, method, path string, body []byte, contentType string) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	} else if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	return req, nil
}

func (c *Client) doRaw(ctx context.Context, method, path string, body []byte, contentType string) ([]byte, error) {
	req, err := c.newRequest(ctx, method, path, body, contentType)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	return raw, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body []byte
	var contentType string
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
		contentType = "application/json"
	}
	raw, err := c.doRaw(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	if output == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, output)
}

func (c *Client) doNDJSON(ctx context.Context, path string, ndjson []byte, output any) error {
	raw, err := c.doRaw(ctx, http.MethodPost, path, ndjson, "application/x-ndjson")
	if err != nil {
		return err
	}
	if output == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, output)
}

func (c *Client) resolveAlias(ctx context.Context, alias string) (string, bool, error) {
	var payload map[string]any
	err := c.doJSON(ctx, http.MethodGet, "/_alias/"+url.PathEscape(alias), nil, &payload)
	if isNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(payload) == 0 {
		return "", false, nil
	}
	names := make([]string, 0, len(payload))
	for name := range payload {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[len(names)-1], true, nil
}

func (c *Client) detectVectorDimensions(ctx context.Context, indexName string) (int, error) {
	var payload map[string]struct {
		Mappings struct {
			Properties struct {
				EmbeddingVec struct {
					Dimension int `json:"dimension"`
				} `json:"embedding_vec"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/"+url.PathEscape(indexName)+"/_mapping", nil, &payload); err != nil {
		return 0, err
	}
	entry, ok := payload[indexName]
	if !ok {
		return 0, nil
	}
	return entry.Mappings.Properties.EmbeddingVec.Dimension, nil
}

func buildIndexBody(vector sessionstore.VectorConfig) map[string]any {
	indexSettings := map[string]any{
		"number_of_shards":   1,
		"number_of_replicas": 0,
	}
	properties := map[string]any{
		"id":           map[string]any{"type": "keyword"},
		"kind":         map[string]any{"type": "keyword"},
		"run_id":       map[string]any{"type": "keyword"},
		"task_id":      map[string]any{"type": "keyword"},
		"parent_id":    map[string]any{"type": "keyword"},
		"role":         map[string]any{"type": "keyword"},
		"model":        map[string]any{"type": "keyword"},
		"effort":       map[string]any{"type": "keyword"},
		"status":       map[string]any{"type": "keyword"},
		"scope":        map[string]any{"type": "keyword"},
		"refs":         map[string]any{"type": "keyword"},
		"tags":         map[string]any{"type": "keyword"},
		"workspace_id": map[string]any{"type": "keyword"},
		"entity_id":    map[string]any{"type": "keyword"},
		"post_id":      map[string]any{"type": "keyword"},
		"summary":      map[string]any{"type": "text"},
		"content":      map[string]any{"type": "text"},
		"search_text":  map[string]any{"type": "text"},
		"created_at":   map[string]any{"type": "date"},
		"updated_at":   map[string]any{"type": "date"},
		"record_raw":   map[string]any{"type": "keyword", "index": false, "doc_values": false},
	}
	if vector.Enabled() {
		indexSettings["knn"] = true
		properties["embedding_vec"] = map[string]any{
			"type":       "knn_vector",
			"dimension":  vector.Dimensions,
			"space_type": "cosinesimil",
		}
		properties["vector_projections"] = map[string]any{
			"type": "nested",
			"properties": map[string]any{
				"id": map[string]any{"type": "keyword"},
				"vector": map[string]any{
					"type":       "knn_vector",
					"dimension":  vector.Dimensions,
					"space_type": "cosinesimil",
				},
			},
		}
	}
	return map[string]any{
		"settings": map[string]any{"index": indexSettings},
		"mappings": map[string]any{
			"dynamic":    false,
			"properties": properties,
		},
	}
}

func (c *Client) createIndex(ctx context.Context, indexName string, vector sessionstore.VectorConfig) error {
	return c.doJSON(ctx, http.MethodPut, "/"+url.PathEscape(indexName), buildIndexBody(vector), nil)
}

func (c *Client) putAlias(ctx context.Context, indexName, alias string) error {
	body := map[string]any{
		"actions": []map[string]any{
			{"add": map[string]any{"index": indexName, "alias": alias}},
		},
	}
	return c.doJSON(ctx, http.MethodPost, "/_aliases", body, nil)
}

func (c *Client) swapAlias(ctx context.Context, oldIndex, newIndex, alias string) error {
	actions := []map[string]any{}
	if oldIndex != "" {
		actions = append(actions, map[string]any{"remove": map[string]any{"index": oldIndex, "alias": alias}})
	}
	actions = append(actions, map[string]any{"add": map[string]any{"index": newIndex, "alias": alias}})
	return c.doJSON(ctx, http.MethodPost, "/_aliases", map[string]any{"actions": actions}, nil)
}

func (c *Client) deleteIndex(ctx context.Context, indexName string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/"+url.PathEscape(indexName), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}
