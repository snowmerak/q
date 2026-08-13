package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	ServiceName     = "q-library"
	ProtocolVersion = 1
	Implementation  = "0.1.0"
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
}

func NewClient(endpoint, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = time.Duration(defaultProbeTimeout) * time.Millisecond
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"), apiKey: apiKey,
		http: &http.Client{Timeout: timeout},
	}
}

func (c *Client) Endpoint() string { return c.endpoint }

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
