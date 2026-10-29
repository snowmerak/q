// Package embeddingbridge lets an ACP stdio bridge use its owning Q host's
// existing Gateway for query embeddings, without starting another provider.
package embeddingbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/fsreplace"
)

type Connection struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
}

func Write(path string, connection Connection) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(connection)
	if err != nil {
		return err
	}
	return fsreplace.WriteFile(path, raw, 0o600)
}

type Embedder struct {
	Path       string
	Store      config.Store
	Model      string
	Dimensions int
}

func (e *Embedder) Embed(ctx context.Context, request client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	check := func() error {
		value, err := e.Store.Load()
		if err != nil {
			return err
		}
		if value.Embedding.Model != e.Model || value.Embedding.Dimensions != e.Dimensions {
			return errors.New("embedding settings changed; retry the query")
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	if request.Model != e.Model {
		return nil, errors.New("query embedding model does not match current settings")
	}
	raw, err := os.ReadFile(e.Path)
	if err != nil {
		return nil, errors.New("host embedding connection is unavailable")
	}
	if len(raw) > 16384 {
		return nil, errors.New("invalid host embedding connection")
	}
	var connection Connection
	if err := json.Unmarshal(raw, &connection); err != nil {
		return nil, errors.New("invalid host embedding connection")
	}
	endpoint, err := url.Parse(connection.Endpoint)
	if err != nil || endpoint.Scheme != "http" || net.ParseIP(endpoint.Hostname()) == nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || connection.APIKey == "" {
		return nil, errors.New("host embedding Gateway is not ready")
	}
	provider, err := client.New(client.Config{BaseURL: connection.Endpoint, APIKey: connection.APIKey, HTTPClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, ForwardUsageMetadata: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = provider.Close() }()
	result, err := provider.Embed(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	return result, nil
}
