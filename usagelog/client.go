package usagelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	qclient "github.com/snowmerak/q/client"
)

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(endpoint string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: &http.Client{Timeout: timeout}}
}

func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var output Health
	err := c.doJSON(ctx, http.MethodGet, "/v1/health", nil, &output)
	return output, err
}

func (c *Client) Append(ctx context.Context, record qclient.UsageRecord) error {
	var output struct {
		Inserted bool `json:"inserted"`
	}
	return c.doJSON(ctx, http.MethodPost, "/v1/events", record, &output)
}

func (c *Client) Query(ctx context.Context, filter Filter) (UsageView, error) {
	query := url.Values{}
	if !filter.From.IsZero() {
		query.Set("from", filter.From.UTC().Format(time.RFC3339))
	}
	if !filter.To.IsZero() {
		query.Set("to", filter.To.UTC().Format(time.RFC3339))
	}
	if filter.Model != "" {
		query.Set("model", filter.Model)
	}
	if filter.Role != "" {
		query.Set("role", filter.Role)
	}
	path := "/api/v1/usage"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var output UsageView
	err := c.doJSON(ctx, http.MethodGet, path, nil, &output)
	return output, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	if c == nil || c.http == nil {
		return errors.New("usage: client is unavailable")
	}
	if ctx == nil {
		return errors.New("usage: request context is nil")
	}
	var body *bytes.Reader
	if input == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
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
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		return fmt.Errorf("usage: HTTP %d %s: %s", response.StatusCode, envelope.Error.Code, envelope.Error.Message)
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("usage: decode %s: %w", path, err)
	}
	return nil
}
