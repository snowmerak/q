package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const responseCachePrefix = "cache_"

type responseDocument struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Output []json.RawMessage `json:"output"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
		InputDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

func responseAffinity(conversationID string) (string, error) {
	if strings.HasPrefix(conversationID, responseCachePrefix) {
		return conversationID, nil
	}
	var entropy [18]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("client: generate Responses cache affinity: %w", err)
	}
	return responseCachePrefix + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

func (c *Client) responseRequest(request ChatRequest, affinity string) (json.RawMessage, error) {
	if len(request.Stop) > 0 {
		return nil, errors.New("client: Responses mode does not support Chat stop sequences")
	}
	input, err := responseInput(request.Messages, request.Model)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{
		"model": request.Model, "input": input, "store": false,
	}
	if request.OutputSchema != nil {
		fields["text"] = map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "q_output", "strict": true, "schema": request.OutputSchema,
		}}
	}
	if request.ReasoningEffort != "" {
		fields["reasoning"] = map[string]any{"effort": request.ReasoningEffort}
	}
	if request.Temperature != nil {
		fields["temperature"] = request.Temperature
	}
	if request.TopP != nil {
		fields["top_p"] = request.TopP
	}
	if request.MaxCompletionTokens != nil {
		fields["max_output_tokens"] = request.MaxCompletionTokens
	} else if request.MaxTokens != nil {
		fields["max_output_tokens"] = request.MaxTokens
	}
	if request.ParallelToolCalls != nil {
		fields["parallel_tool_calls"] = request.ParallelToolCalls
	}
	if len(request.Tools) > 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Type != ToolTypeFunction {
				return nil, fmt.Errorf("client: Responses mode does not support tool type %q", tool.Type)
			}
			item := map[string]any{"type": "function", "name": tool.Function.Name}
			if tool.Function.Description != "" {
				item["description"] = tool.Function.Description
			}
			if tool.Function.Parameters != nil {
				item["parameters"] = tool.Function.Parameters
			}
			if tool.Function.Strict != nil {
				item["strict"] = *tool.Function.Strict
			}
			tools = append(tools, item)
		}
		fields["tools"] = tools
	}
	if request.ToolChoice != nil {
		choice := request.ToolChoice
		if named, ok := choice.(map[string]any); ok && named["type"] == "function" {
			if function, ok := named["function"].(map[string]string); ok {
				choice = map[string]any{"type": "function", "name": function["name"]}
			} else if function, ok := named["function"].(map[string]any); ok {
				choice = map[string]any{"type": "function", "name": function["name"]}
			}
		}
		fields["tool_choice"] = choice
	}
	for key, value := range request.Extra {
		if key != "stream_options" {
			if _, reserved := fields[key]; reserved {
				return nil, fmt.Errorf("client: Responses extra field %q conflicts with a typed field", key)
			}
			fields[key] = value
		}
	}
	if c.forwardUsageMetadata {
		fields["cache_affinity_id"] = affinity
	}
	return json.Marshal(fields)
}

func responseInput(messages []Message, model string) ([]any, error) {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == RoleTool {
			if message.ToolCallID == "" {
				return nil, errors.New("client: Responses tool result has no call ID")
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.TextContent()})
			continue
		}
		if message.Role == RoleAssistant && len(message.ResponseOutput) > 0 && message.ResponseModel == model {
			for _, raw := range message.ResponseOutput {
				var item any
				if err := json.Unmarshal(raw, &item); err != nil {
					return nil, fmt.Errorf("client: decode previous Responses item: %w", err)
				}
				input = append(input, item)
			}
			continue
		}
		if message.Role != RoleSystem && message.Role != RoleDeveloper && message.Role != RoleUser && message.Role != RoleAssistant {
			return nil, fmt.Errorf("client: Responses mode does not support message role %q", message.Role)
		}
		content, err := responseMessageContent(message)
		if err != nil {
			return nil, err
		}
		if content != nil {
			item := map[string]any{"role": message.Role, "content": content}
			if message.Role == RoleAssistant {
				phase := message.Phase
				if phase == "" && len(message.ToolCalls) > 0 {
					phase = "commentary"
				} else if phase == "" {
					phase = "final_answer"
				}
				item["phase"] = phase
			}
			input = append(input, item)
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				return nil, errors.New("client: Responses previous function call has no call ID")
			}
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	return input, nil
}

func responseMessageContent(message Message) (any, error) {
	if len(message.ContentParts) == 0 {
		if message.Content == "" {
			return nil, nil
		}
		return message.Content, nil
	}
	parts := make([]map[string]any, 0, len(message.ContentParts))
	for _, part := range message.ContentParts {
		switch part["type"] {
		case "text", "input_text", "output_text":
			typeName := "input_text"
			if message.Role == RoleAssistant {
				typeName = "output_text"
			}
			parts = append(parts, map[string]any{"type": typeName, "text": part["text"]})
		case "image_url", "input_image":
			if message.Role != RoleUser {
				return nil, errors.New("client: Responses images require a user message")
			}
			image := map[string]any{"type": "input_image"}
			if value, ok := part["image_url"].(map[string]any); ok {
				image["image_url"] = value["url"]
				if detail, found := value["detail"]; found {
					image["detail"] = detail
				}
			} else if value, ok := part["image_url"].(string); ok {
				image["image_url"] = value
			} else if fileID, ok := part["file_id"].(string); ok {
				image["file_id"] = fileID
			} else {
				return nil, errors.New("client: Responses image is missing a URL or file ID")
			}
			parts = append(parts, image)
		default:
			return nil, fmt.Errorf("client: Responses content type %q is unsupported", part["type"])
		}
	}
	return parts, nil
}

func parseResponseDocument(body json.RawMessage, affinity, requestedModel string) (*ChatResponse, error) {
	var document responseDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("client: decode Responses reply: %w", err)
	}
	if document.Error != nil {
		return nil, fmt.Errorf("client: Responses failed: %s", document.Error.Message)
	}
	if document.Status != "completed" {
		return nil, fmt.Errorf("client: Responses ended with status %q", document.Status)
	}
	if document.Output == nil {
		return nil, errors.New("client: Responses reply has no output array")
	}
	message := Message{Role: RoleAssistant, ResponseOutput: append([]json.RawMessage(nil), document.Output...), ResponseModel: requestedModel}
	for _, raw := range document.Output {
		var item struct {
			Type      string `json:"type"`
			Phase     string `json:"phase"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("client: decode Responses output item: %w", err)
		}
		switch item.Type {
		case "message":
			if item.Phase == "commentary" {
				continue
			}
			if item.Phase != "" {
				message.Phase = item.Phase
			}
			for _, part := range item.Content {
				if part.Type == "output_text" {
					message.Content += part.Text
				}
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" {
				return nil, errors.New("client: Responses function call is missing call_id or name")
			}
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				Index: len(message.ToolCalls), ID: item.CallID, Type: ToolTypeFunction,
				Function: FunctionCall{Name: item.Name, Arguments: item.Arguments},
			})
		}
	}
	if message.Content == "" && len(message.ToolCalls) == 0 {
		return nil, errors.New("client: Responses completed without text or function calls")
	}
	usage := Usage{PromptTokens: document.Usage.InputTokens, CompletionTokens: document.Usage.OutputTokens, TotalTokens: document.Usage.TotalTokens}
	usage.PromptDetails = &TokenDetails{CachedTokens: document.Usage.InputDetails.CachedTokens, CacheWriteTokens: document.Usage.InputDetails.CacheWriteTokens}
	finishReason := "stop"
	if len(message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	return &ChatResponse{
		ID: document.ID, Object: "chat.completion", Model: document.Model,
		ConversationID: affinity, Usage: usage, Raw: append(json.RawMessage(nil), body...),
		Choices: []Choice{{Index: 0, Message: message, FinishReason: finishReason}},
	}, nil
}

func (c *Client) responsesChat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	affinity, err := responseAffinity(request.ConversationID)
	if err != nil {
		return nil, err
	}
	body, err := c.responseRequest(request, affinity)
	if err != nil {
		return nil, err
	}
	headers := request.Headers.Clone()
	if c.forwardUsageMetadata {
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set("X-Q-Require-Native-Responses", "true")
	}
	ctx, headers = c.withUsageMetadata(ctx, headers, "")
	raw, err := c.provider.CreateResponse(ctx, body, headers)
	if err != nil {
		return nil, err
	}
	response, err := parseResponseDocument(raw.Body, affinity, request.Model)
	if response != nil {
		response.Headers = raw.Headers
		c.recordChatUsage(ctx, request, response.Model, response.Usage, estimateResponseTokens(response))
	}
	return response, err
}

func (c *Client) responsesChatStream(ctx context.Context, request ChatRequest) (Stream, error) {
	affinity, err := responseAffinity(request.ConversationID)
	if err != nil {
		return nil, err
	}
	body, err := c.responseRequest(request, affinity)
	if err != nil {
		return nil, err
	}
	headers := request.Headers.Clone()
	if c.forwardUsageMetadata {
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set("X-Q-Require-Native-Responses", "true")
	}
	ctx, headers = c.withUsageMetadata(ctx, headers, "")
	raw, err := c.provider.CreateResponseStream(ctx, body, headers)
	if err != nil {
		return nil, err
	}
	stream := &responsesChunkStream{inner: raw, model: request.Model, affinity: affinity}
	if c.usageRecorder == nil {
		return stream, nil
	}
	tracked := &usageStream{inner: stream, client: c, request: request, role: usageRole(ctx), eventID: usageEventID(ctx)}
	if headers, ok := raw.(ResponseHeaderer); ok {
		return &usageHeaderStream{usageStream: tracked, headers: headers}, nil
	}
	return tracked, nil
}

type responsesChunkStream struct {
	inner     ResponseStream
	model     string
	affinity  string
	text      strings.Builder
	completed bool
	output    map[int]json.RawMessage
	phases    map[int]string
}

func (s *responsesChunkStream) Close() error { return s.inner.Close() }

func (s *responsesChunkStream) ResponseHeaders() http.Header {
	if headers, ok := s.inner.(ResponseHeaderer); ok {
		return headers.ResponseHeaders()
	}
	return nil
}

func (s *responsesChunkStream) Recv() (*ChatChunk, error) {
	if s.completed {
		return nil, io.EOF
	}
	for {
		event, err := s.inner.Recv()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("client: Responses stream ended without response.completed")
		}
		if err != nil {
			return nil, err
		}
		var data struct {
			Type        string          `json:"type"`
			Delta       string          `json:"delta"`
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
			Response    json.RawMessage `json:"response"`
			Error       *struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return nil, fmt.Errorf("client: decode Responses stream event: %w", err)
		}
		kind := data.Type
		if kind == "" {
			kind = event.Event
		}
		switch kind {
		case "response.output_item.added":
			var item struct {
				Phase string `json:"phase"`
			}
			if len(data.Item) > 0 {
				if err := json.Unmarshal(data.Item, &item); err != nil {
					return nil, fmt.Errorf("client: decode Responses added item: %w", err)
				}
			}
			if s.phases == nil {
				s.phases = make(map[int]string)
			}
			s.phases[data.OutputIndex] = item.Phase
		case "response.output_text.delta":
			phase := s.phases[data.OutputIndex]
			if phase != "commentary" {
				s.text.WriteString(data.Delta)
			}
			return &ChatChunk{Model: s.model, Choices: []Choice{{Index: 0, Phase: phase, Delta: &Message{Role: RoleAssistant, Content: data.Delta}}}}, nil
		case "response.reasoning_summary_text.delta":
			return &ChatChunk{Model: s.model, Choices: []Choice{{Index: 0, Phase: "commentary", Delta: &Message{Role: RoleAssistant, Content: data.Delta}}}}, nil
		case "response.output_item.done":
			if s.output == nil {
				s.output = make(map[int]json.RawMessage)
			}
			s.output[data.OutputIndex] = append(json.RawMessage(nil), data.Item...)
		case "response.failed", "response.incomplete", "error":
			message := data.Message
			if data.Error != nil && data.Error.Message != "" {
				message = data.Error.Message
			}
			return nil, fmt.Errorf("client: Responses stream %s: %s", kind, message)
		case "response.completed":
			body := data.Response
			if len(body) == 0 {
				return nil, errors.New("client: Responses completion event has no response")
			}
			if len(s.output) > 0 {
				var payload map[string]json.RawMessage
				if json.Unmarshal(body, &payload) == nil && (len(payload["output"]) == 0 || string(payload["output"]) == "null" || string(payload["output"]) == "[]") {
					indexes := make([]int, 0, len(s.output))
					for index := range s.output {
						indexes = append(indexes, index)
					}
					sort.Ints(indexes)
					items := make([]json.RawMessage, 0, len(indexes))
					for _, index := range indexes {
						items = append(items, s.output[index])
					}
					payload["output"], _ = json.Marshal(items)
					body, _ = json.Marshal(payload)
				}
			}
			response, err := parseResponseDocument(body, s.affinity, s.model)
			if err != nil {
				return nil, err
			}
			message := response.Choices[0].Message
			if !strings.HasPrefix(message.Content, s.text.String()) {
				return nil, errors.New("client: Responses completed text differs from streamed text")
			}
			message.Content = strings.TrimPrefix(message.Content, s.text.String())
			s.completed = true
			return &ChatChunk{
				ID: response.ID, Model: response.Model, ConversationID: response.ConversationID, Usage: &response.Usage,
				Choices: []Choice{{Index: 0, Delta: &message, FinishReason: response.Choices[0].FinishReason}},
			}, nil
		}
	}
}
