// Package anthropic adapts the Claude Messages API to llmprovider's common
// OpenAI-compatible chat surface.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

const (
	defaultBaseURL          = "https://api.anthropic.com/v1"
	defaultAnthropicVersion = "2023-06-01"
	defaultMaxTokens        = 65536
)

type config struct {
	baseURL          string
	apiKey           string
	anthropicVersion string
	httpClient       *http.Client
	headers          http.Header
	body             map[string]any
}

type Option func(*config)

func WithBaseURL(baseURL string) Option { return func(c *config) { c.baseURL = baseURL } }
func WithAPIKey(apiKey string) Option   { return func(c *config) { c.apiKey = apiKey } }
func WithAnthropicVersion(version string) Option {
	return func(c *config) { c.anthropicVersion = version }
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
func WithBodyField(key string, value any) Option {
	return func(c *config) {
		if key != "" {
			c.body[key] = value
		}
	}
}

type Provider struct{ config config }

func New(opts ...Option) *Provider {
	apiKey := os.Getenv("CLAUDE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	cfg := config{
		baseURL: defaultBaseURL, apiKey: apiKey, anthropicVersion: defaultAnthropicVersion,
		httpClient: http.DefaultClient, headers: make(http.Header), body: make(map[string]any),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Provider{config: cfg}
}

func (p *Provider) Chat(ctx context.Context, request llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	response, err := p.doMessages(ctx, request, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read message: %w", err)
	}
	var message messageResponse
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, fmt.Errorf("anthropic: decode message: %w", err)
	}
	result, err := message.toChatResponse()
	if err != nil {
		return nil, err
	}
	result.Headers = response.Header.Clone()
	result.Raw = append(result.Raw[:0], data...)
	return result, nil
}

func (p *Provider) ChatStream(ctx context.Context, request llmprovider.ChatRequest) (llmprovider.Stream, error) {
	response, err := p.doMessages(ctx, request, true)
	if err != nil {
		return nil, err
	}
	return newSSEStream(response.Body, response.Header.Clone()), nil
}

func (p *Provider) ListModels(ctx context.Context) ([]llmprovider.Model, error) {
	response, err := p.doRequest(ctx, http.MethodGet, "/models?limit=1000", nil, nil, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	type anthropicModel struct {
		ID             string `json:"id"`
		Type           string `json:"type"`
		CreatedAt      string `json:"created_at"`
		MaxInputTokens int64  `json:"max_input_tokens"`
		MaxTokens      int64  `json:"max_tokens"`
		Capabilities   struct {
			Effort json.RawMessage `json:"effort"`
		} `json:"capabilities"`
	}
	var envelope struct {
		Data []anthropicModel `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("anthropic: decode model list: %w", err)
	}
	models := make([]llmprovider.Model, 0, len(envelope.Data))
	for _, upstream := range envelope.Data {
		created := int64(0)
		if upstream.CreatedAt != "" {
			createdAt, err := time.Parse(time.RFC3339, upstream.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("anthropic: decode model %q created_at: %w", upstream.ID, err)
			}
			created = createdAt.Unix()
		}
		object := upstream.Type
		if object == "" {
			object = "model"
		}
		reasoningEfforts := supportedAnthropicReasoningEfforts(upstream.Capabilities.Effort)
		defaultReasoningEffort := ""
		for _, effort := range reasoningEfforts {
			if effort == "high" {
				defaultReasoningEffort = "high"
				break
			}
		}
		var capabilities *llmprovider.ModelCapabilities
		if len(reasoningEfforts) > 0 {
			capabilities = &llmprovider.ModelCapabilities{Reasoning: &llmprovider.ReasoningCapabilities{
				Supported: true, Control: llmprovider.ReasoningControlEffort,
				SupportedEfforts: reasoningEfforts, DefaultEffort: defaultReasoningEffort,
			}}
		}
		models = append(models, llmprovider.Model{
			ID: upstream.ID, Object: object, Created: created, OwnedBy: "anthropic",
			ContextLength: upstream.MaxInputTokens, MaxOutputTokens: upstream.MaxTokens,
			Capabilities: capabilities,
		})
	}
	return models, nil
}

func supportedAnthropicReasoningEfforts(data json.RawMessage) []string {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(data, &capabilities) != nil {
		return nil
	}
	efforts := make([]string, 0, len(capabilities))
	for name, raw := range capabilities {
		if name == "supported" {
			continue
		}
		var capability struct {
			Supported bool `json:"supported"`
		}
		if json.Unmarshal(raw, &capability) == nil && capability.Supported {
			efforts = append(efforts, name)
		}
	}
	order := map[string]int{"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}
	sort.Slice(efforts, func(i, j int) bool {
		left, leftKnown := order[efforts[i]]
		right, rightKnown := order[efforts[j]]
		if leftKnown && rightKnown {
			return left < right
		}
		if leftKnown != rightKnown {
			return leftKnown
		}
		return efforts[i] < efforts[j]
	})
	return efforts
}

func (p *Provider) Close() error { return nil }

func (p *Provider) doMessages(ctx context.Context, request llmprovider.ChatRequest, stream bool) (*http.Response, error) {
	payload, err := p.messagePayload(request, stream)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode message: %w", err)
	}
	return p.doRequest(ctx, http.MethodPost, "/messages", bytes.NewReader(body), request.Headers, stream)
}

func (p *Provider) messagePayload(request llmprovider.ChatRequest, stream bool) (map[string]any, error) {
	if request.Model == "" {
		return nil, errors.New("anthropic: model is required")
	}
	if len(request.Messages) == 0 {
		return nil, errors.New("anthropic: at least one message is required")
	}
	if request.Model == "claude-sonnet-5" {
		if request.Temperature != nil && *request.Temperature != 1 {
			return nil, errors.New("anthropic: claude-sonnet-5 does not accept non-default temperature")
		}
		if request.TopP != nil && *request.TopP != 1 {
			return nil, errors.New("anthropic: claude-sonnet-5 does not accept non-default top_p")
		}
	}
	messages, system, err := convertMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	payload := make(map[string]any, len(p.config.body)+len(request.Extra)+12)
	for key, value := range p.config.body {
		payload[key] = value
	}
	for key, value := range request.Extra {
		if key == "stream_options" {
			// OpenAI-compatible callers request streamed usage with this field.
			// Messages always emits usage and rejects the OpenAI-only option.
			continue
		}
		if key == "cache_control" && value == nil {
			delete(payload, key)
			continue
		}
		payload[key] = value
	}
	payload["model"] = request.Model
	payload["messages"] = messages
	payload["stream"] = stream
	maxTokens := defaultMaxTokens
	if request.MaxCompletionTokens != nil {
		maxTokens = *request.MaxCompletionTokens
	} else if request.MaxTokens != nil {
		maxTokens = *request.MaxTokens
	}
	payload["max_tokens"] = maxTokens
	if len(system) > 0 {
		payload["system"] = system
	}
	if request.Temperature != nil {
		payload["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		payload["top_p"] = *request.TopP
	}
	if len(request.Stop) > 0 {
		payload["stop_sequences"] = request.Stop
	}
	if request.ReasoningEffort != "" {
		payload["output_config"] = map[string]any{"effort": request.ReasoningEffort}
	}
	if request.OutputSchema != nil {
		outputConfig, _ := payload["output_config"].(map[string]any)
		if outputConfig == nil {
			outputConfig = make(map[string]any)
		}
		outputConfig["format"] = request.OutputSchema
		payload["output_config"] = outputConfig
	}
	tools, toolChoice, err := convertTools(request)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	if toolChoice != nil {
		payload["tool_choice"] = toolChoice
	}
	return payload, nil
}

func convertMessages(input []llmprovider.Message) ([]map[string]any, []map[string]any, error) {
	messages := make([]map[string]any, 0, len(input))
	system := make([]map[string]any, 0)
	appendMessage := func(role string, blocks []map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
			previous := messages[len(messages)-1]["content"].([]map[string]any)
			messages[len(messages)-1]["content"] = append(previous, blocks...)
			return
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}
	for _, message := range input {
		if message.Role == llmprovider.RoleSystem || message.Role == llmprovider.RoleDeveloper {
			system = append(system, contentBlocks(message)...)
			continue
		}
		if message.Role == llmprovider.RoleTool {
			appendMessage("user", []map[string]any{{
				"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.TextContent(),
			}})
			continue
		}
		role := "user"
		if message.Role == llmprovider.RoleAssistant {
			role = "assistant"
		}
		blocks := contentBlocks(message)
		for _, call := range message.ToolCalls {
			var arguments any = map[string]any{}
			if call.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
					return nil, nil, fmt.Errorf("anthropic: invalid arguments for tool %q: %w", call.Function.Name, err)
				}
			}
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": arguments,
			})
		}
		appendMessage(role, blocks)
	}
	return messages, system, nil
}

func contentBlocks(message llmprovider.Message) []map[string]any {
	if len(message.ContentParts) == 0 {
		if message.Content == "" {
			return nil
		}
		return []map[string]any{{"type": "text", "text": message.Content}}
	}
	blocks := make([]map[string]any, 0, len(message.ContentParts))
	for _, part := range message.ContentParts {
		block := make(map[string]any, len(part))
		for key, value := range part {
			block[key] = value
		}
		switch block["type"] {
		case "input_text", "output_text":
			block["type"] = "text"
		}
		if marker, ok := block["prompt_cache_breakpoint"].(map[string]any); ok {
			cacheControl := map[string]any{"type": "ephemeral"}
			if ttl, exists := marker["ttl"]; exists {
				cacheControl["ttl"] = ttl
			}
			block["cache_control"] = cacheControl
			delete(block, "prompt_cache_breakpoint")
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func convertTools(request llmprovider.ChatRequest) ([]map[string]any, map[string]any, error) {
	if len(request.Tools) == 0 {
		return nil, nil, nil
	}
	if mode, ok := request.ToolChoice.(llmprovider.ToolChoiceMode); ok && mode == llmprovider.ToolChoiceNone {
		return nil, nil, nil
	}
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Type != llmprovider.ToolTypeFunction || tool.Function.Name == "" {
			return nil, nil, errors.New("anthropic: only named function tools are supported")
		}
		schema := any(tool.Function.Parameters)
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		converted := map[string]any{
			"name": tool.Function.Name, "description": tool.Function.Description, "input_schema": schema,
		}
		if tool.Function.Strict != nil {
			converted["strict"] = *tool.Function.Strict
		}
		tools = append(tools, converted)
	}
	choice := map[string]any{"type": "auto"}
	switch value := request.ToolChoice.(type) {
	case nil:
	case llmprovider.ToolChoiceMode:
		if value == llmprovider.ToolChoiceRequired {
			choice["type"] = "any"
		}
	case string:
		if value == string(llmprovider.ToolChoiceRequired) {
			choice["type"] = "any"
		}
	case map[string]any:
		name, _ := value["name"].(string)
		switch function := value["function"].(type) {
		case map[string]any:
			name, _ = function["name"].(string)
		case map[string]string:
			name = function["name"]
		}
		if name == "" {
			return nil, nil, errors.New("anthropic: named tool choice requires a name")
		}
		choice = map[string]any{"type": "tool", "name": name}
	default:
		return nil, nil, errors.New("anthropic: invalid tool choice")
	}
	if request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		choice["disable_parallel_tool_use"] = true
	}
	return tools, choice, nil
}

func (p *Provider) doRequest(ctx context.Context, method, path string, body io.Reader, requestHeaders http.Header, stream bool) (*http.Response, error) {
	endpoint := strings.TrimRight(p.config.baseURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	for key, values := range p.config.headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	for key, values := range requestHeaders {
		if isReservedHeader(key) {
			continue
		}
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	}
	request.Header.Set("anthropic-version", p.config.anthropicVersion)
	if p.config.apiKey != "" {
		request.Header.Set("x-api-key", p.config.apiKey)
	}
	response, err := p.config.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("anthropic: request: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return nil, fmt.Errorf("anthropic: API error (HTTP %d): %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return response, nil
}

func isReservedHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "X-Api-Key", "Anthropic-Version", "Content-Type", "Accept", "Host":
		return true
	default:
		return false
	}
}

type messageResponse struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Role       string          `json:"role"`
	Content    []responseBlock `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      usageResponse   `json:"usage"`
}

type responseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type usageResponse struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (m messageResponse) toChatResponse() (*llmprovider.ChatResponse, error) {
	message := llmprovider.Message{Role: llmprovider.RoleAssistant}
	for _, block := range m.Content {
		switch block.Type {
		case "text":
			message.Content += block.Text
		case "tool_use":
			arguments := string(block.Input)
			if arguments == "" || arguments == "null" {
				arguments = "{}"
			}
			message.ToolCalls = append(message.ToolCalls, llmprovider.ToolCall{
				Index: len(message.ToolCalls), ID: block.ID, Type: llmprovider.ToolTypeFunction,
				Function: llmprovider.FunctionCall{Name: block.Name, Arguments: arguments},
			})
		}
	}
	usage := normalizeUsage(m.Usage)
	return &llmprovider.ChatResponse{
		ID: m.ID, Object: "chat.completion", Model: m.Model,
		Choices: []llmprovider.Choice{{
			Index: 0, Message: message, FinishReason: finishReason(m.StopReason),
		}},
		Usage: usage,
	}, nil
}

func normalizeUsage(value usageResponse) llmprovider.Usage {
	prompt := value.InputTokens + value.CacheCreationInputTokens + value.CacheReadInputTokens
	return llmprovider.Usage{
		PromptTokens: prompt, CompletionTokens: value.OutputTokens, TotalTokens: prompt + value.OutputTokens,
		PromptDetails: &llmprovider.TokenDetails{
			CachedTokens: value.CacheReadInputTokens, CacheWriteTokens: value.CacheCreationInputTokens,
		},
	}
}

func finishReason(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "end_turn", "stop_sequence", "refusal":
		return "stop"
	default:
		return reason
	}
}

var _ llmprovider.Provider = (*Provider)(nil)
var _ llmprovider.ModelLister = (*Provider)(nil)
