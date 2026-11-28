// Package openai implements the OpenAI-compatible chat completions provider.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

const defaultBaseURL = "https://api.openai.com/v1"

type config struct {
	baseURL               string
	apiKey                string
	httpClient            *http.Client
	headers               http.Header
	body                  map[string]any
	reasoningEffortObject bool
}

type Option func(*config)

func WithBaseURL(baseURL string) Option {
	return func(c *config) { c.baseURL = baseURL }
}

func WithAPIKey(apiKey string) Option {
	return func(c *config) { c.apiKey = apiKey }
}

func WithHTTPClient(client *http.Client) Option {
	return func(c *config) {
		if client != nil {
			c.httpClient = client
		}
	}
}

func WithHeader(key, value string) Option {
	return func(c *config) { c.headers.Set(key, value) }
}

// WithBodyField sets a provider-level default request field. Request-scoped
// Extra values override it, and typed ChatRequest fields remain authoritative.
// This supports provider extensions such as cache_control,
// prompt_cache_options, provider routing, and session_id.
func WithBodyField(key string, value any) Option {
	return func(c *config) {
		if key != "" {
			c.body[key] = value
		}
	}
}

// WithReasoningEffortObject maps ChatRequest.ReasoningEffort to
// reasoning.effort instead of the OpenAI reasoning_effort field. OpenRouter
// uses this request shape for Chat Completions.
func WithReasoningEffortObject() Option {
	return func(c *config) { c.reasoningEffortObject = true }
}

type Provider struct {
	config config
}

func New(opts ...Option) *Provider {
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	config := config{
		baseURL:    baseURL,
		apiKey:     os.Getenv("OPENAI_API_KEY"),
		httpClient: http.DefaultClient,
		headers:    make(http.Header),
		body:       make(map[string]any),
	}
	for _, opt := range opts {
		opt(&config)
	}
	return &Provider{config: config}
}

func (p *Provider) Chat(ctx context.Context, request llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	response, err := p.do(ctx, request, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read chat completion: %w", err)
	}
	var result llmprovider.ChatResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("openai: decode chat completion: %w", err)
	}
	result.Raw = append(result.Raw[:0], data...)
	result.Headers = response.Header.Clone()
	return &result, nil
}

// ListModels returns models exposed by the configured OpenAI-compatible
// endpoint.
func (p *Provider) ListModels(ctx context.Context) ([]llmprovider.Model, error) {
	response, err := p.doRequest(ctx, http.MethodGet, "/models", nil, nil, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var envelope struct {
		Data []llmprovider.Model `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("openai: decode model list: %w", err)
	}
	return envelope.Data, nil
}

func (p *Provider) ChatStream(ctx context.Context, request llmprovider.ChatRequest) (llmprovider.Stream, error) {
	response, err := p.do(ctx, request, true)
	if err != nil {
		return nil, err
	}
	return &sseStream{body: response.Body, reader: bufio.NewReader(response.Body), headers: response.Header.Clone()}, nil
}

// Embed calls the OpenAI-compatible embeddings endpoint. Model discovery is
// deliberately shared with chat models: an embedding model returned by
// /models is immediately routable without a second configuration list.
func (p *Provider) Embed(ctx context.Context, request llmprovider.EmbeddingRequest) (*llmprovider.EmbeddingResponse, error) {
	payload := make(map[string]any, len(p.config.body)+len(request.Extra)+5)
	for key, value := range p.config.body {
		payload[key] = value
	}
	for key, value := range request.Extra {
		payload[key] = value
	}
	payload["model"] = request.Model
	payload["input"] = request.Input
	if request.EncodingFormat != "" {
		payload["encoding_format"] = request.EncodingFormat
	}
	setOptional(payload, "dimensions", request.Dimensions)
	if request.User != "" {
		payload["user"] = request.User
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: encode embeddings request: %w", err)
	}
	response, err := p.doRequest(ctx, http.MethodPost, "/embeddings", bytes.NewReader(body), request.Headers, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read embeddings response: %w", err)
	}
	var result llmprovider.EmbeddingResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("openai: decode embeddings response: %w", err)
	}
	result.Raw = append(result.Raw[:0], data...)
	result.Headers = response.Header.Clone()
	return &result, nil
}

func (p *Provider) CreateResponse(ctx context.Context, body json.RawMessage, headers http.Header) (*llmprovider.RawResponse, error) {
	encoded, err := p.responseBody(body, false)
	if err != nil {
		return nil, err
	}
	response, err := p.doRequest(ctx, http.MethodPost, "/responses", bytes.NewReader(encoded), headers, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read responses response: %w", err)
	}
	return &llmprovider.RawResponse{Body: data, Headers: response.Header.Clone()}, nil
}

func (p *Provider) CreateResponseStream(ctx context.Context, body json.RawMessage, headers http.Header) (llmprovider.ResponseStream, error) {
	encoded, err := p.responseBody(body, true)
	if err != nil {
		return nil, err
	}
	response, err := p.doRequest(ctx, http.MethodPost, "/responses", bytes.NewReader(encoded), headers, true)
	if err != nil {
		return nil, err
	}
	return &rawSSEStream{body: response.Body, reader: bufio.NewReader(response.Body), headers: response.Header.Clone()}, nil
}

func (p *Provider) responseBody(body json.RawMessage, stream bool) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("openai: decode responses request: %w", err)
	}
	merged := make(map[string]any, len(p.config.body)+len(payload)+1)
	for key, value := range p.config.body {
		merged[key] = value
	}
	for key, value := range payload {
		merged[key] = value
	}
	merged["stream"] = stream
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("openai: encode responses request: %w", err)
	}
	return encoded, nil
}

func (*Provider) OpenAICompatibleWire() {}

func (p *Provider) Close() error { return nil }

func (p *Provider) do(ctx context.Context, request llmprovider.ChatRequest, stream bool) (*http.Response, error) {
	if len(request.Messages) == 0 {
		return nil, errors.New("openai: at least one message is required")
	}
	payload := make(map[string]any, len(p.config.body)+len(request.Extra)+12)
	for key, value := range p.config.body {
		payload[key] = value
	}
	for key, value := range request.Extra {
		payload[key] = value
	}
	payload["model"] = request.Model
	payload["messages"] = request.Messages
	payload["stream"] = stream
	if request.ConversationID != "" {
		payload["conversation_id"] = request.ConversationID
	}
	setOptional(payload, "temperature", request.Temperature)
	setOptional(payload, "top_p", request.TopP)
	setOptional(payload, "max_tokens", request.MaxTokens)
	setOptional(payload, "max_completion_tokens", request.MaxCompletionTokens)
	setOptional(payload, "parallel_tool_calls", request.ParallelToolCalls)
	if request.ReasoningEffort != "" {
		if p.config.reasoningEffortObject {
			reasoning := make(map[string]any)
			if configured, ok := payload["reasoning"].(map[string]any); ok {
				for key, value := range configured {
					reasoning[key] = value
				}
			}
			reasoning["effort"] = request.ReasoningEffort
			payload["reasoning"] = reasoning
		} else {
			payload["reasoning_effort"] = request.ReasoningEffort
		}
	}
	if len(request.Stop) > 0 {
		payload["stop"] = request.Stop
	}
	if len(request.Tools) > 0 {
		payload["tools"] = request.Tools
	}
	if request.ToolChoice != nil {
		payload["tool_choice"] = request.ToolChoice
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: encode chat completion: %w", err)
	}
	return p.doRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body), request.Headers, stream)
}

func (p *Provider) doRequest(ctx context.Context, method, path string, body io.Reader, requestHeaders http.Header, stream bool) (*http.Response, error) {
	endpoint := strings.TrimRight(p.config.baseURL, "/") + path
	httpRequest, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("openai: create %s request: %w", path, err)
	}
	for key, values := range p.config.headers {
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}
	for key, values := range requestHeaders {
		if isReservedHeader(key) {
			continue
		}
		httpRequest.Header.Del(key)
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if stream {
		httpRequest.Header.Set("Accept", "text/event-stream")
	}
	if p.config.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.config.apiKey)
	}

	response, err := p.config.httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("openai: %s request: %w", path, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return nil, fmt.Errorf("openai: HTTP %d (read error: %v)", response.StatusCode, readErr)
		}
		return nil, decodeAPIError(response.StatusCode, data)
	}
	return response, nil
}

func isReservedHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Authorization", "Content-Type", "Accept", "Host":
		return true
	default:
		return false
	}
}

func setOptional[T any](payload map[string]any, key string, value *T) {
	if value != nil {
		payload[key] = *value
	}
}

type APIError struct {
	StatusCode int
	Type       string
	Code       any
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("openai: API error (HTTP %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("openai: API error (HTTP %d): %s", e.StatusCode, e.Body)
}

func (e *APIError) HTTPStatusCode() int     { return e.StatusCode }
func (e *APIError) APIErrorMessage() string { return e.Message }
func (e *APIError) APIErrorType() string    { return e.Type }
func (e *APIError) APIErrorCode() any       { return e.Code }

func decodeAPIError(status int, data []byte) *APIError {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(data, &envelope)
	var details struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(envelope.Error, &details); err != nil {
		_ = json.Unmarshal(envelope.Error, &details.Message)
	}
	return &APIError{StatusCode: status, Type: details.Type, Code: details.Code, Message: details.Message, Body: strings.TrimSpace(string(data))}
}

type sseStream struct {
	body    io.ReadCloser
	reader  *bufio.Reader
	headers http.Header
	mu      sync.Mutex
	done    bool
}

type rawSSEStream struct {
	body    io.ReadCloser
	reader  *bufio.Reader
	headers http.Header
	mu      sync.Mutex
	done    bool
}

func (s *rawSSEStream) ResponseHeaders() http.Header { return s.headers.Clone() }

func (s *rawSSEStream) Recv() (*llmprovider.ResponseEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil, io.EOF
	}
	for {
		event, data, err := readSSEEvent(s.reader)
		if err != nil {
			s.done = true
			_ = s.body.Close()
			return nil, err
		}
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			s.done = true
			_ = s.body.Close()
			return nil, io.EOF
		}
		return &llmprovider.ResponseEvent{Event: event, Data: json.RawMessage(data)}, nil
	}
}

func (s *rawSSEStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	return s.body.Close()
}

func (s *sseStream) ResponseHeaders() http.Header {
	return s.headers.Clone()
}

func (s *sseStream) Recv() (*llmprovider.ChatChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil, io.EOF
	}
	for {
		data, err := readSSEData(s.reader)
		if err != nil {
			s.done = true
			_ = s.body.Close()
			return nil, err
		}
		if data == "[DONE]" {
			s.done = true
			_ = s.body.Close()
			return nil, io.EOF
		}
		if data == "" {
			continue
		}
		var frame struct {
			llmprovider.ChatChunk
			Error json.RawMessage `json:"error"`
		}
		payload := []byte(data)
		if err := json.Unmarshal(payload, &frame); err != nil {
			return nil, fmt.Errorf("openai: decode stream chunk: %w", err)
		}
		if len(frame.Error) > 0 && !bytes.Equal(frame.Error, []byte("null")) {
			s.done = true
			_ = s.body.Close()
			apiError := decodeAPIError(http.StatusBadGateway, payload)
			if status, ok := apiError.Code.(float64); ok && status >= 400 && status <= 599 && float64(int(status)) == status {
				apiError.StatusCode = int(status)
			}
			return nil, apiError
		}
		frame.Raw = payload
		return &frame.ChatChunk, nil
	}
}

func (s *sseStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	return s.body.Close()
}

func readSSEData(reader *bufio.Reader) (string, error) {
	_, data, err := readSSEEvent(reader)
	return data, err
}

func readSSEEvent(reader *bufio.Reader) (string, string, error) {
	var event string
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			if errors.Is(err, io.EOF) && len(lines) > 0 {
				return event, strings.Join(lines, "\n"), nil
			}
			return "", "", err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return event, strings.Join(lines, "\n"), nil
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			lines = append(lines, value)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return event, strings.Join(lines, "\n"), nil
			}
			return "", "", err
		}
	}
}

var _ llmprovider.Provider = (*Provider)(nil)
var _ llmprovider.ModelLister = (*Provider)(nil)
var _ llmprovider.Embedder = (*Provider)(nil)
var _ llmprovider.ResponsesProvider = (*Provider)(nil)
var _ llmprovider.OpenAIWireProvider = (*Provider)(nil)
