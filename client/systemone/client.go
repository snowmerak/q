// Package systemone provides a native HTTP client for Jev-style System One decisions.
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/snowmerak/decisions"
)

const (
	DefaultBaseURL   = "https://system-one.dev/v1"
	maxRequestBytes  = 65536
	maxResponseBytes = 1 << 20
)

// Config selects a System One endpoint and an optional platform API key.
// BaseURL defaults to DefaultBaseURL. An empty APIKey sends no authorization.
type Config struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// API defaults to the native System One protocol.
	API decisions.API
	// ModelFilterRegex matches upstream model IDs during discovery.
	ModelFilterRegex string
}

// Client sends native System One requests. It is safe to share between goroutines.
type Client struct {
	baseURL        string
	apiKey         string
	httpClient     *http.Client
	decisionConfig decisions.Config
	decisionModels *decisions.Client
	modelFilter    *regexp.Regexp
}

func New(config Config) (*Client, error) {
	baseURL := config.BaseURL
	translated := config.API != "" && config.API != decisions.SystemOneAPI
	if baseURL == "" && !translated {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if baseURL != "" && (err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "") {
		return nil, errors.New("systemone: BaseURL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	key := config.APIKey
	if key != "" && (strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n")) {
		return nil, errors.New("systemone: API key must be nonblank and contain no newline")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	// Redirects can change the request destination. Never forward the platform
	// credential to a redirect target, including one chosen by a custom client.
	copyClient := *httpClient
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: key, httpClient: &copyClient}
	if config.ModelFilterRegex != "" {
		client.modelFilter, err = regexp.Compile(config.ModelFilterRegex)
		if err != nil {
			return nil, fmt.Errorf("systemone: model_filter_regex: %w", err)
		}
	}
	if translated {
		client.decisionConfig = decisions.Config{API: config.API, BaseURL: baseURL, APIKey: key, HTTPClient: &copyClient}
		client.decisionModels, err = decisions.NewClient(client.decisionConfig)
		if err != nil {
			return nil, err
		}
	}
	return client, nil
}

// Evaluate marshals a typed request and sends one decision operation. It does
// not perform application-level retries: callers must retain the same key and
// request when recovering an uncertain outcome. An empty key makes this a
// separate billable operation.
func (c *Client) Evaluate(ctx context.Context, request Request, options CallOptions) (*Result, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("systemone: encode request: %w", err)
	}
	return c.EvaluateRaw(ctx, body, options)
}

// EvaluateRaw forwards the JSON unchanged to native System One providers.
// Other providers receive the supported questions translated to their protocol.
func (c *Client) EvaluateRaw(ctx context.Context, body json.RawMessage, options CallOptions) (result *Result, returnErr error) {
	if len(body) == 0 || len(body) > maxRequestBytes || !json.Valid(body) || bytes.TrimSpace(body)[0] != '{' {
		return nil, errors.New("systemone: request must be a JSON object of at most 65536 bytes")
	}
	if err := validateIdempotencyKey(options.IdempotencyKey); err != nil {
		return nil, err
	}
	var selected struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &selected)
	finish := logDecision(ctx, selected.Model)
	defer func() { finish(result, returnErr) }()
	if c != nil && c.decisionModels != nil {
		return c.evaluateDecision(ctx, body, options)
	}
	responseBody, header, err := c.do(ctx, http.MethodPost, "systemone", body, options.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	var decoded Result
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil, fmt.Errorf("systemone: decode decision response: %w", err)
	}
	decoded.Raw = responseBody
	decoded.Header = header
	return &decoded, nil
}

// ListModels returns the endpoint's model catalog. Versioned model IDs can be
// valid even when they do not appear in this list.
func (c *Client) ListModels(ctx context.Context) (*ModelsResult, error) {
	if c != nil && c.decisionModels != nil {
		return c.listDecisionModels(ctx)
	}
	responseBody, header, err := c.do(ctx, http.MethodGet, "models", nil, "")
	if err != nil {
		return nil, err
	}
	var result ModelsResult
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("systemone: decode model catalog: %w", err)
	}
	if c.modelFilter != nil {
		result.Models = slices.DeleteFunc(result.Models, func(model Model) bool { return !c.allowsModel(model.Name) })
	}
	result.Raw = responseBody
	result.Header = header
	return &result, nil
}

func (c *Client) allowsModel(id string) bool {
	return c.modelFilter == nil || c.modelFilter.MatchString(id)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, idempotencyKey string) (json.RawMessage, http.Header, error) {
	if c == nil {
		return nil, nil, errors.New("systemone: nil client")
	}
	endpoint, err := url.JoinPath(c.baseURL, path)
	if err != nil {
		return nil, nil, fmt.Errorf("systemone: build endpoint: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("systemone: create request: %w", err)
	}
	if method == http.MethodPost {
		// net/http may transparently replay a POST with Idempotency-Key when
		// GetBody is set. Leave recovery decisions to the caller.
		request.GetBody = nil
	}
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		if idempotencyKey != "" {
			request.Header.Set("Idempotency-Key", idempotencyKey)
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("systemone: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("systemone: read response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return nil, nil, errors.New("systemone: response exceeds 1 MiB")
	}
	header := response.Header.Clone()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, header, &APIError{
			StatusCode: response.StatusCode,
			Code:       header.Get("X-System-One-Error-Code"),
			Source:     header.Get("X-System-One-Error-Source"),
			RequestID:  header.Get("X-Request-Id"),
			RetryAfter: header.Get("Retry-After"),
			Header:     header,
			Body:       responseBody,
		}
	}
	return responseBody, header, nil
}

func validateIdempotencyKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 128 {
		return errors.New("systemone: Idempotency-Key must be at most 128 characters")
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		alphanumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphanumeric && (index == 0 || character != '.' && character != '_' && character != ':' && character != '-') {
			return errors.New("systemone: invalid Idempotency-Key")
		}
	}
	return nil
}

// ValidateIdempotencyKey applies the native request header rules.
func ValidateIdempotencyKey(key string) error {
	return validateIdempotencyKey(key)
}
