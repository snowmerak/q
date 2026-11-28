package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/providers/openai"
)

func (p *Provider) transport(ctx context.Context) (*openai.Provider, error) {
	token, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	return openai.New(openai.WithBaseURL(resource), openai.WithAPIKey(token), openai.WithHTTPClient(p.options.HTTPClient),
		openai.WithHeader("originator", p.options.AppID), openai.WithHeader("User-Agent", p.options.AppID+"/llm-provider")), nil
}

func (p *Provider) ListModels(ctx context.Context) ([]llmprovider.Model, error) {
	token, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, resource+"/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("originator", p.options.AppID)
	response, err := p.options.HTTPClient.Do(request)
	if err != nil {
		return nil, errors.New("chatgpt: model catalog request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("chatgpt: model catalog HTTP %d", response.StatusCode)
	}
	var catalog struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return nil, err
	}
	models := make([]llmprovider.Model, 0, len(catalog.Models))
	for _, raw := range catalog.Models {
		var picker struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
			Reasoning  []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			DefaultReasoning string `json:"default_reasoning_level"`
		}
		if err := json.Unmarshal(raw, &picker); err != nil {
			return nil, err
		}
		if picker.Slug == "" || picker.Visibility != "list" {
			continue
		}
		var model llmprovider.Model
		if err := json.Unmarshal(raw, &model); err != nil {
			return nil, err
		}
		model.ID, model.Object, model.OwnedBy = picker.Slug, "model", "chatgpt"
		if len(picker.Reasoning) > 0 {
			efforts := make([]string, 0, len(picker.Reasoning))
			for _, level := range picker.Reasoning {
				if level.Effort != "" {
					efforts = append(efforts, level.Effort)
				}
			}
			model.Capabilities = &llmprovider.ModelCapabilities{Reasoning: &llmprovider.ReasoningCapabilities{Supported: true, Control: llmprovider.ReasoningControlEffort, SupportedEfforts: efforts, DefaultEffort: picker.DefaultReasoning}}
		}
		models = append(models, model)
	}
	return models, nil
}

// normalize keeps the full stateless context and fails unsupported options
// explicitly. It never silently removes an output limit or switches billing.
func normalize(body json.RawMessage) (json.RawMessage, error) {
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, errors.New("chatgpt: Responses body must be an object")
	}
	for _, name := range []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation", "multi_agent", "prompt", "prompt_cache_retention", "safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user", "previous_response_id"} {
		if value, ok := fields[name]; ok && value != nil {
			return nil, fmt.Errorf("chatgpt: ChatGPT plan usage does not support %s", name)
		}
		delete(fields, name)
	}
	if text, ok := fields["input"].(string); ok {
		fields["input"] = []any{map[string]any{"role": "user", "content": text}}
	}
	input, ok := fields["input"].([]any)
	if !ok {
		return nil, errors.New("chatgpt: input must be an array or text")
	}
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("chatgpt: input item must be an object")
		}
		if item["role"] == "system" {
			item["role"] = "developer"
		}
	}
	if raw, ok := fields["tools"]; ok {
		tools, ok := raw.([]any)
		if !ok {
			return nil, errors.New("chatgpt: tools must be an array")
		}
		var flat []any
		normalized := make([]any, 0, len(tools)+1)
		hasFunctionsNamespace := false
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("chatgpt: tool must be an object")
			}
			switch tool["type"] {
			case "function", "custom":
				flat = append(flat, tool)
			case "namespace":
				children, ok := tool["tools"].([]any)
				if !ok {
					return nil, errors.New("chatgpt: namespace tools must be an array")
				}
				for _, child := range children {
					value, ok := child.(map[string]any)
					if !ok || value["type"] != "function" && value["type"] != "custom" {
						return nil, errors.New("chatgpt: namespace contains an unsupported tool")
					}
				}
				if tool["name"] == "functions" {
					hasFunctionsNamespace = true
				}
				normalized = append(normalized, tool)
			case "web_search":
				normalized = append(normalized, tool)
			default:
				return nil, fmt.Errorf("chatgpt: unsupported hosted tool %v", tool["type"])
			}
		}
		if len(flat) > 0 {
			if hasFunctionsNamespace {
				return nil, errors.New("chatgpt: flat tools collide with namespace functions")
			}
			// Namespace descriptions are required by the Responses schema,
			// independently of the descriptions on the contained tools.
			normalized = append(normalized, map[string]any{
				"type": "namespace", "name": "functions",
				"description": "Client-provided tools executed by the application.", "tools": flat,
			})
			if choice, ok := fields["tool_choice"].(map[string]any); ok && choice["type"] == "function" {
				if _, explicit := choice["namespace"]; !explicit {
					choice["namespace"] = "functions"
				}
			}
		}
		fields["tools"] = normalized
	}
	fields["store"], fields["stream"] = false, true
	// Retain opaque reasoning state when replaying the full stateless history.
	var include []any
	if raw, ok := fields["include"]; ok {
		var valid bool
		include, valid = raw.([]any)
		if !valid {
			return nil, errors.New("chatgpt: include must be an array")
		}
	}
	found := false
	for _, value := range include {
		if name, ok := value.(string); ok && name == "reasoning.encrypted_content" {
			found = true
		}
	}
	if !found {
		include = append(include, "reasoning.encrypted_content")
	}
	fields["include"] = include
	return json.Marshal(fields)
}

func (p *Provider) CreateResponseStream(ctx context.Context, body json.RawMessage, headers http.Header) (llmprovider.ResponseStream, error) {
	encoded, err := normalize(body)
	if err != nil {
		return nil, err
	}
	provider, err := p.transport(ctx)
	if err != nil {
		return nil, err
	}
	// The owning application supplies identity; request headers cannot alias it.
	headers = headers.Clone()
	for key := range headers {
		if strings.EqualFold(key, "originator") || strings.EqualFold(key, "User-Agent") {
			delete(headers, key)
		}
	}
	// Plan cache reuse also needs a stable session-id. Derive it from the
	// caller's cache key so stateless requests keep the same session across
	// turns and restarts, without changing other providers or caller headers.
	var cache struct {
		Key string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(encoded, &cache); err != nil {
		return nil, fmt.Errorf("chatgpt: decode prompt cache key: %w", err)
	}
	if cache.Key != "" && headers.Get("session-id") == "" {
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set("session-id", cache.Key)
	}
	stream, err := provider.CreateResponseStream(ctx, encoded, headers)
	if err != nil {
		return nil, err
	}
	return &responseStream{inner: stream}, nil
}

// CreateResponse collects an upstream stream. The public API still supplies a
// normal terminal response to non-streaming Gateway and compaction callers.
func (p *Provider) CreateResponse(ctx context.Context, body json.RawMessage, headers http.Header) (*llmprovider.RawResponse, error) {
	stream, err := p.CreateResponseStream(ctx, body, headers)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	output := make(map[int]json.RawMessage)
	for {
		event, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		var value struct {
			Type        string          `json:"type"`
			Response    json.RawMessage `json:"response"`
			OutputIndex *int            `json:"output_index"`
			Item        json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(event.Data, &value); err != nil {
			return nil, err
		}
		if value.Type == "" {
			value.Type = event.Event
		}
		if value.Type == "response.output_item.done" {
			if value.OutputIndex == nil || *value.OutputIndex < 0 || len(value.Item) == 0 || string(value.Item) == "null" {
				return nil, errors.New("chatgpt: completed output item is missing its index or value")
			}
			output[*value.OutputIndex] = append(json.RawMessage(nil), value.Item...)
		}
		if value.Type == "response.completed" {
			body, err := collectResponseOutput(value.Response, output)
			if err != nil {
				return nil, err
			}
			return &llmprovider.RawResponse{Body: body, Headers: stream.(*responseStream).ResponseHeaders()}, nil
		}
	}
}

// Plan streams may put their full output only in output_item.done events and
// send an empty or partial output array in response.completed. Keep the final
// per-item snapshots, including opaque reasoning, when collecting the stream.
func collectResponseOutput(body json.RawMessage, output map[int]json.RawMessage) (json.RawMessage, error) {
	if len(output) == 0 {
		return body, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("chatgpt: completed response is missing its body")
	}
	var terminal []json.RawMessage
	if raw := document["output"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &terminal); err != nil {
			return nil, err
		}
	}
	ids := make(map[string]bool)
	next := 0
	for index, item := range output {
		var header struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, err
		}
		if header.ID != "" {
			ids[header.ID] = true
		}
		if index >= next {
			next = index + 1
		}
	}
	for index, item := range terminal {
		var header struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, err
		}
		if header.ID != "" && ids[header.ID] {
			continue
		}
		if _, exists := output[index]; exists {
			if header.ID == "" {
				continue
			}
			index = next
			next++
		}
		output[index] = item
		if header.ID != "" {
			ids[header.ID] = true
		}
		if index >= next {
			next = index + 1
		}
	}
	indexes := make([]int, 0, len(output))
	for index := range output {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	items := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		items = append(items, output[index])
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	document["output"] = encoded
	return json.Marshal(document)
}

type responseStream struct {
	inner     llmprovider.ResponseStream
	completed bool
}

type responseErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *responseStream) Close() error { return s.inner.Close() }
func (s *responseStream) ResponseHeaders() http.Header {
	if headers, ok := s.inner.(interface{ ResponseHeaders() http.Header }); ok {
		return headers.ResponseHeaders()
	}
	return nil
}
func (s *responseStream) Recv() (*llmprovider.ResponseEvent, error) {
	if s.completed {
		return nil, io.EOF
	}
	event, err := s.inner.Recv()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("chatgpt: stream ended without response.completed")
	}
	if err != nil {
		return nil, err
	}
	var value struct {
		Type     string               `json:"type"`
		Code     string               `json:"code"`
		Message  string               `json:"message"`
		Error    *responseErrorDetail `json:"error"`
		Response struct {
			Status            string               `json:"status"`
			Error             *responseErrorDetail `json:"error"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if err := json.Unmarshal(event.Data, &value); err != nil {
		return nil, err
	}
	if value.Type == "" {
		value.Type = event.Event
	}
	switch value.Type {
	case "response.completed":
		s.completed = true
		if value.Response.Status != "completed" {
			return nil, errors.New("chatgpt: terminal response did not complete")
		}
	case "response.failed", "response.incomplete", "error":
		s.completed = true
		failure := value.Response.Error
		if failure == nil {
			failure = value.Error
		}
		code, message := value.Code, value.Message
		if failure != nil {
			code, message = failure.Code, failure.Message
		}
		details := []string{value.Type}
		if code != "" {
			details = append(details, code)
		}
		if message != "" {
			details = append(details, message)
		}
		if value.Response.IncompleteDetails != nil && value.Response.IncompleteDetails.Reason != "" {
			details = append(details, value.Response.IncompleteDetails.Reason)
		}
		return nil, errors.New("chatgpt: " + strings.Join(details, ": "))
	}
	return event, nil
}

func chatBody(request llmprovider.ChatRequest) (json.RawMessage, error) {
	if len(request.Stop) > 0 {
		return nil, errors.New("chatgpt: stop sequences are not supported")
	}
	fields := make(map[string]any)
	for key, value := range request.Extra {
		// Chat's include_usage option is not a Responses option. Responses
		// already reports usage in its completion event for both caller modes.
		if key == "stream_options" {
			continue
		}
		fields[key] = value
	}
	fields["model"] = request.Model
	input := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role == llmprovider.RoleTool {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.TextContent()})
			continue
		}
		if message.Role == llmprovider.RoleAssistant && message.ResponseModel == request.Model && len(message.ResponseOutput) > 0 {
			for _, raw := range message.ResponseOutput {
				input = append(input, raw)
			}
			continue
		}
		var content any = message.Content
		if len(message.ContentParts) > 0 {
			parts := make([]any, 0, len(message.ContentParts))
			for _, part := range message.ContentParts {
				switch part["type"] {
				case "text":
					parts = append(parts, map[string]any{"type": "input_text", "text": part["text"]})
				case "image_url":
					image, ok := part["image_url"].(map[string]any)
					if !ok {
						return nil, errors.New("chatgpt: invalid image URL")
					}
					converted := map[string]any{"type": "input_image", "image_url": image["url"]}
					if detail, ok := image["detail"]; ok {
						converted["detail"] = detail
					}
					parts = append(parts, converted)
				default:
					parts = append(parts, part)
				}
			}
			content = parts
		}
		if message.Content != "" || len(message.ContentParts) > 0 {
			item := map[string]any{"role": message.Role, "content": content}
			if message.Phase != "" {
				item["phase"] = message.Phase
			}
			input = append(input, item)
		}
		for _, call := range message.ToolCalls {
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments, "namespace": "functions"})
		}
	}
	fields["input"] = input
	if request.Temperature != nil {
		fields["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		fields["top_p"] = *request.TopP
	}
	if request.MaxTokens != nil {
		fields["max_output_tokens"] = *request.MaxTokens
	}
	if request.MaxCompletionTokens != nil {
		fields["max_output_tokens"] = *request.MaxCompletionTokens
	}
	if request.ReasoningEffort != "" {
		fields["reasoning"] = map[string]any{"effort": request.ReasoningEffort}
	}
	if request.OutputSchema != nil {
		fields["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "response", "strict": true, "schema": request.OutputSchema}}
	}
	if request.ParallelToolCalls != nil {
		fields["parallel_tool_calls"] = *request.ParallelToolCalls
	}
	if len(request.Tools) > 0 {
		tools := make([]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Type != llmprovider.ToolTypeFunction {
				return nil, errors.New("chatgpt: unsupported Chat tool type")
			}
			value := map[string]any{"type": "function", "name": tool.Function.Name, "parameters": tool.Function.Parameters}
			if tool.Function.Description != "" {
				value["description"] = tool.Function.Description
			}
			if tool.Function.Strict != nil {
				value["strict"] = *tool.Function.Strict
			}
			tools = append(tools, value)
		}
		fields["tools"] = tools
	}
	if request.ToolChoice != nil {
		choice := request.ToolChoice
		if named, ok := choice.(map[string]any); ok && named["type"] == "function" {
			switch fn := named["function"].(type) {
			case map[string]any:
				choice = map[string]any{"type": "function", "name": fn["name"]}
			case map[string]string:
				choice = map[string]any{"type": "function", "name": fn["name"]}
			}
		}
		fields["tool_choice"] = choice
	}
	return json.Marshal(fields)
}

func responseChat(body json.RawMessage) (*llmprovider.ChatResponse, error) {
	var document struct {
		ID     string            `json:"id"`
		Model  string            `json:"model"`
		Output []json.RawMessage `json:"output"`
		Usage  struct {
			Input   int                       `json:"input_tokens"`
			Output  int                       `json:"output_tokens"`
			Total   int                       `json:"total_tokens"`
			Details *llmprovider.TokenDetails `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	message := llmprovider.Message{Role: llmprovider.RoleAssistant, ResponseOutput: document.Output, ResponseModel: document.Model}
	for _, raw := range document.Output {
		var item struct {
			Type      string `json:"type"`
			Phase     string `json:"phase"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		if item.Type == "function_call" {
			message.ToolCalls = append(message.ToolCalls, llmprovider.ToolCall{Index: len(message.ToolCalls), ID: item.CallID, Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionCall{Name: item.Name, Arguments: item.Arguments}})
		}
		if item.Type == "message" && item.Phase != "commentary" {
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					message.Content += part.Text
				case "refusal":
					message.Content += part.Refusal
				}
			}
		}
	}
	finish := "stop"
	if len(message.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	return &llmprovider.ChatResponse{ID: document.ID, Object: "chat.completion", Model: document.Model, Choices: []llmprovider.Choice{{Message: message, FinishReason: finish}}, Usage: llmprovider.Usage{PromptTokens: document.Usage.Input, CompletionTokens: document.Usage.Output, TotalTokens: document.Usage.Total, PromptDetails: document.Usage.Details}}, nil
}

func (p *Provider) Chat(ctx context.Context, request llmprovider.ChatRequest) (*llmprovider.ChatResponse, error) {
	body, err := chatBody(request)
	if err != nil {
		return nil, err
	}
	raw, err := p.CreateResponse(ctx, body, request.Headers)
	if err != nil {
		return nil, err
	}
	response, err := responseChat(raw.Body)
	if response != nil {
		response.Headers = raw.Headers
		response.Choices[0].Message.ResponseModel = request.Model
	}
	return response, err
}
func (p *Provider) ChatStream(ctx context.Context, request llmprovider.ChatRequest) (llmprovider.Stream, error) {
	body, err := chatBody(request)
	if err != nil {
		return nil, err
	}
	stream, err := p.CreateResponseStream(ctx, body, request.Headers)
	if err != nil {
		return nil, err
	}
	return &chatStream{inner: stream, model: request.Model, phases: make(map[int]string)}, nil
}

type chatStream struct {
	inner     llmprovider.ResponseStream
	model     string
	text      strings.Builder
	phases    map[int]string
	output    map[int]json.RawMessage
	completed bool
}

func (s *chatStream) Close() error { return s.inner.Close() }
func (s *chatStream) ResponseHeaders() http.Header {
	if h, ok := s.inner.(interface{ ResponseHeaders() http.Header }); ok {
		return h.ResponseHeaders()
	}
	return nil
}
func (s *chatStream) Recv() (*llmprovider.ChatChunk, error) {
	if s.completed {
		return nil, io.EOF
	}
	for {
		event, err := s.inner.Recv()
		if err != nil {
			return nil, err
		}
		var value struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Index    *int            `json:"output_index"`
			Item     json.RawMessage `json:"item"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(event.Data, &value); err != nil {
			return nil, err
		}
		if value.Type == "" {
			value.Type = event.Event
		}
		index := 0
		if value.Index != nil {
			index = *value.Index
		}
		switch value.Type {
		case "response.output_item.added":
			var item struct {
				Phase string `json:"phase"`
			}
			if err := json.Unmarshal(value.Item, &item); err != nil {
				return nil, err
			}
			s.phases[index] = item.Phase
		case "response.output_text.delta", "response.refusal.delta":
			phase := s.phases[index]
			if phase != "commentary" {
				s.text.WriteString(value.Delta)
			}
			return &llmprovider.ChatChunk{Model: s.model, Choices: []llmprovider.Choice{{Phase: phase, Delta: &llmprovider.Message{Role: llmprovider.RoleAssistant, Content: value.Delta}}}}, nil
		case "response.output_item.done":
			if value.Index == nil || index < 0 || len(value.Item) == 0 || string(value.Item) == "null" {
				return nil, errors.New("chatgpt: completed output item is missing its index or value")
			}
			if s.output == nil {
				s.output = make(map[int]json.RawMessage)
			}
			s.output[index] = append(json.RawMessage(nil), value.Item...)
		case "response.completed":
			body, err := collectResponseOutput(value.Response, s.output)
			if err != nil {
				return nil, err
			}
			response, err := responseChat(body)
			if err != nil {
				return nil, err
			}
			message := response.Choices[0].Message
			message.ResponseModel = s.model
			if !strings.HasPrefix(message.Content, s.text.String()) {
				return nil, errors.New("chatgpt: terminal text differs from the streamed output")
			}
			message.Content = strings.TrimPrefix(message.Content, s.text.String())
			s.completed = true
			return &llmprovider.ChatChunk{ID: response.ID, Model: response.Model, Usage: &response.Usage, Choices: []llmprovider.Choice{{Delta: &message, FinishReason: response.Choices[0].FinishReason}}}, nil
		}
	}
}

var _ llmprovider.Provider = (*Provider)(nil)
var _ llmprovider.ResponsesProvider = (*Provider)(nil)
