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
	err := c.doJSON(ctx, http.MethodPost, "/v1/events", record, &output)
	if record.Performance != nil && unknownParameter(err, `unknown field "performance"`) {
		return nil
	}
	if record.Attribution != (qclient.UsageAttribution{}) && unknownParameter(err, `unknown field "attribution"`) {
		if record.Performance != nil {
			return nil
		}
		record.Attribution = qclient.UsageAttribution{}
		return c.doJSON(ctx, http.MethodPost, "/v1/events", record, &output)
	}
	return err
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
	if filter.Details {
		query.Set("details", "1")
	}
	path := "/api/v1/usage"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var output UsageView
	err := c.doJSON(ctx, http.MethodGet, path, nil, &output)
	if filter.Details && unknownParameter(err, `unknown query parameter "details"`) {
		filter.Details = false
		return c.Query(ctx, filter)
	}
	return output, err
}

type usageHTTPError struct {
	status        int
	code, message string
}

func (err *usageHTTPError) Error() string {
	return fmt.Sprintf("usage: HTTP %d %s: %s", err.status, err.code, err.message)
}

func unknownParameter(err error, message string) bool {
	response, ok := errors.AsType[*usageHTTPError](err)
	return ok && response.status == http.StatusBadRequest && strings.Contains(response.message, message)
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
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		return &usageHTTPError{status: response.StatusCode, code: envelope.Error.Code, message: envelope.Error.Message}
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
