package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

var responseIDSequence atomic.Uint64

type responseRequest struct {
	Model             string             `json:"model"`
	Input             any                `json:"input"`
	Instructions      string             `json:"instructions,omitempty"`
	Tools             []map[string]any   `json:"tools,omitempty"`
	ToolChoice        any                `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64           `json:"temperature,omitempty"`
	TopP              *float64           `json:"top_p,omitempty"`
	MaxOutputTokens   *int               `json:"max_output_tokens,omitempty"`
	Reasoning         *responseReasoning `json:"reasoning,omitempty"`
	Stream            bool               `json:"stream,omitempty"`
}

type responseReasoning struct {
	Effort string `json:"effort,omitempty"`
}

func (g *Gateway) handleResponses(writer http.ResponseWriter, request *http.Request) {
	data, decoded, err := decodeResponseRequest(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	route, backendModel, err := g.resolve(decoded.Model)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	externalModel := decoded.Model
	backendBody, err := replaceRequestModel(data, backendModel)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	backendBody, err = prepareResponseCache(route, backendBody)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	headers := selectedHeaders(request.Header, route.forwardHeaders)
	headers.Del("X-Q-Require-Native-Responses")
	if native, ok := route.provider.(llmprovider.ResponsesProvider); ok {
		if decoded.Stream {
			g.streamNativeResponse(writer, request.Context(), route, native, backendBody, headers, externalModel)
			return
		}
		response, err := native.CreateResponse(request.Context(), backendBody, headers)
		if err != nil {
			writeProviderError(writer, err)
			return
		}
		copySelectedHeaders(writer.Header(), response.Headers, route.forwardResponseHeaders)
		writeRawJSON(writer, http.StatusOK, rewriteTopLevelModel(response.Body, externalModel))
		return
	}
	if request.Header.Get("X-Q-Require-Native-Responses") == "true" {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("model %q does not support native Responses", externalModel))
		return
	}

	chatRequest, err := responseToChat(decoded)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	chatRequest.Model = backendModel
	chatRequest.Headers = headers
	if decoded.Stream {
		g.streamAdaptedResponse(writer, request.Context(), route, externalModel, decoded, chatRequest)
		return
	}
	chatResponse, err := route.provider.Chat(request.Context(), chatRequest)
	if err != nil {
		writeProviderError(writer, err)
		return
	}
	copySelectedHeaders(writer.Header(), chatResponse.Headers, route.forwardResponseHeaders)
	writeJSON(writer, http.StatusOK, buildResponseObject(externalModel, decoded, chatResponse))
}

// cache_affinity_id is a Gateway-only extension. It never reaches the
// upstream Responses API, including when a provider has no cache mechanism.
func prepareResponseCache(route *route, body []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("decode responses request: %w", err)
	}
	var affinity string
	if raw, found := fields["cache_affinity_id"]; found {
		if err := json.Unmarshal(raw, &affinity); err != nil {
			return nil, errors.New("cache_affinity_id must be a string")
		}
		delete(fields, "cache_affinity_id")
	}
	if affinity != "" && !route.responseCacheConfigured {
		key := ""
		switch promptCacheMechanism(route) {
		case "openai", "grok", "chatgpt":
			key = "prompt_cache_key"
		case "openrouter":
			key = "session_id"
		}
		if key != "" {
			if _, explicit := fields[key]; !explicit {
				fields[key], _ = json.Marshal(affinity)
			}
		}
	}
	if promptCacheMechanism(route) == "openrouter" && !route.responseCacheControlConfigured {
		var model string
		if err := json.Unmarshal(fields["model"], &model); err == nil && strings.HasPrefix(model, "anthropic/claude-") {
			if _, explicit := fields["cache_control"]; !explicit {
				fields["cache_control"] = json.RawMessage(`{"type":"ephemeral"}`)
			}
		}
	}
	return json.Marshal(fields)
}

func decodeResponseRequest(reader io.Reader) ([]byte, responseRequest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return nil, responseRequest{}, fmt.Errorf("read request: %w", err)
	}
	var decoded responseRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, responseRequest{}, fmt.Errorf("decode request: %w", err)
	}
	if decoded.Model == "" {
		return nil, responseRequest{}, errors.New("model is required")
	}
	if decoded.Input == nil {
		return nil, responseRequest{}, errors.New("input is required")
	}
	return data, decoded, nil
}

func replaceRequestModel(data []byte, model string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}
	payload["model"] = model
	return json.Marshal(payload)
}

func responseToChat(request responseRequest) (llmprovider.ChatRequest, error) {
	messages := make([]llmprovider.Message, 0, 4)
	if request.Instructions != "" {
		messages = append(messages, llmprovider.Message{Role: llmprovider.RoleDeveloper, Content: request.Instructions})
	}
	inputMessages, err := responseInputMessages(request.Input)
	if err != nil {
		return llmprovider.ChatRequest{}, err
	}
	messages = append(messages, inputMessages...)
	tools, err := responseTools(request.Tools)
	if err != nil {
		return llmprovider.ChatRequest{}, err
	}
	toolChoice := request.ToolChoice
	if choice, ok := toolChoice.(map[string]any); ok && choice["type"] == "function" {
		if name, _ := choice["name"].(string); name != "" {
			toolChoice = llmprovider.NamedToolChoice(name)
		}
	}
	reasoningEffort := ""
	if request.Reasoning != nil {
		reasoningEffort = request.Reasoning.Effort
	}
	return llmprovider.ChatRequest{
		Messages: messages, Temperature: request.Temperature, TopP: request.TopP,
		MaxCompletionTokens: request.MaxOutputTokens, Tools: tools, ToolChoice: toolChoice,
		ParallelToolCalls: request.ParallelToolCalls, ReasoningEffort: reasoningEffort,
	}, nil
}

func responseInputMessages(input any) ([]llmprovider.Message, error) {
	if text, ok := input.(string); ok {
		return []llmprovider.Message{{Role: llmprovider.RoleUser, Content: text}}, nil
	}
	items, ok := input.([]any)
	if !ok {
		return nil, errors.New("input must be a string or an array of input items")
	}
	messages := make([]llmprovider.Message, 0, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("input item must be an object")
		}
		typeName, _ := item["type"].(string)
		if typeName == "function_call_output" {
			callID, _ := item["call_id"].(string)
			output, err := responseContentText(item["output"])
			if err != nil {
				return nil, fmt.Errorf("function_call_output: %w", err)
			}
			messages = append(messages, llmprovider.Message{Role: llmprovider.RoleTool, ToolCallID: callID, Content: output})
			continue
		}
		roleText, _ := item["role"].(string)
		role := llmprovider.Role(roleText)
		if role == "" {
			role = llmprovider.RoleUser
		}
		content, parts, err := responseMessageContent(item["content"])
		if err != nil {
			return nil, fmt.Errorf("%s input content: %w", role, err)
		}
		messages = append(messages, llmprovider.Message{Role: role, Content: content, ContentParts: parts})
	}
	return messages, nil
}

func responseMessageContent(content any) (string, []llmprovider.MessageContentPart, error) {
	parts, ok := content.([]any)
	if !ok {
		text, err := responseContentText(content)
		return text, nil, err
	}
	var text strings.Builder
	messageParts := make([]llmprovider.MessageContentPart, 0, len(parts))
	hasImage := false
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return "", nil, errors.New("content part must be an object")
		}
		typeName, _ := part["type"].(string)
		switch typeName {
		case "input_text", "text", "output_text":
			value, _ := part["text"].(string)
			text.WriteString(value)
			messageParts = append(messageParts, llmprovider.MessageContentPart{"type": "text", "text": value})
		case "input_image":
			var image llmprovider.MessageContentPart
			if url, ok := part["image_url"].(string); ok && url != "" {
				imageURL := map[string]any{"url": url}
				if detail, ok := part["detail"]; ok {
					imageURL["detail"] = detail
				}
				image = llmprovider.MessageContentPart{"type": "image_url", "image_url": imageURL}
			} else if fileID, ok := part["file_id"].(string); ok && fileID != "" {
				image = llmprovider.MessageContentPart{"type": "input_image", "file_id": fileID}
				if detail, ok := part["detail"]; ok {
					image["detail"] = detail
				}
			} else {
				return "", nil, errors.New("input_image requires image_url or file_id")
			}
			messageParts = append(messageParts, image)
			hasImage = true
		default:
			return "", nil, fmt.Errorf("content type %q is not supported by the chat adapter", typeName)
		}
	}
	if !hasImage {
		return text.String(), nil, nil
	}
	return text.String(), messageParts, nil
}

func responseContentText(content any) (string, error) {
	if content == nil {
		return "", nil
	}
	if text, ok := content.(string); ok {
		return text, nil
	}
	parts, ok := content.([]any)
	if !ok {
		encoded, err := json.Marshal(content)
		return string(encoded), err
	}
	var text strings.Builder
	for _, value := range parts {
		part, ok := value.(map[string]any)
		if !ok {
			return "", errors.New("content part must be an object")
		}
		typeName, _ := part["type"].(string)
		if typeName != "input_text" && typeName != "text" && typeName != "output_text" {
			return "", fmt.Errorf("content type %q is not supported by the chat adapter", typeName)
		}
		value, _ := part["text"].(string)
		text.WriteString(value)
	}
	return text.String(), nil
}

func responseTools(values []map[string]any) ([]llmprovider.Tool, error) {
	tools := make([]llmprovider.Tool, 0, len(values))
	for _, value := range values {
		if typeName, _ := value["type"].(string); typeName != "function" {
			return nil, fmt.Errorf("tool type %q is not supported by the chat adapter", typeName)
		}
		name, _ := value["name"].(string)
		if name == "" {
			return nil, errors.New("function tool name is required")
		}
		description, _ := value["description"].(string)
		parameters, _ := value["parameters"].(map[string]any)
		var strict *bool
		if strictValue, ok := value["strict"].(bool); ok {
			strict = &strictValue
		}
		tools = append(tools, llmprovider.Tool{Type: llmprovider.ToolTypeFunction, Function: llmprovider.FunctionDefinition{
			Name: name, Description: description, Parameters: parameters, Strict: strict,
		}})
	}
	return tools, nil
}

func newResponseID(prefix string) string {
	return prefix + strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatUint(responseIDSequence.Add(1), 36)
}

func buildResponseObject(model string, request responseRequest, chat *llmprovider.ChatResponse) map[string]any {
	responseID := newResponseID("resp_")
	output := responseOutput(chat, responseID)
	usage := map[string]any{
		"input_tokens": chat.Usage.PromptTokens, "output_tokens": chat.Usage.CompletionTokens,
		"total_tokens": chat.Usage.TotalTokens,
	}
	if chat.Usage.PromptDetails != nil {
		usage["input_tokens_details"] = map[string]any{"cached_tokens": chat.Usage.PromptDetails.CachedTokens}
	}
	return map[string]any{
		"id": responseID, "object": "response", "created_at": time.Now().Unix(), "status": "completed",
		"error": nil, "incomplete_details": nil, "instructions": nullableString(request.Instructions),
		"max_output_tokens": request.MaxOutputTokens, "model": model, "output": output,
		"parallel_tool_calls": boolValue(request.ParallelToolCalls, true), "temperature": request.Temperature,
		"tool_choice": request.ToolChoice, "tools": request.Tools, "top_p": request.TopP,
		"usage": usage,
	}
}

func responseOutput(chat *llmprovider.ChatResponse, responseID string) []any {
	output := make([]any, 0)
	for _, choice := range chat.Choices {
		if choice.Message.Content != "" || len(choice.Message.ContentParts) > 0 {
			output = append(output, map[string]any{
				"id": newResponseID("msg_"), "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": choice.Message.TextContent(), "annotations": []any{}}},
			})
		}
		for _, call := range choice.Message.ToolCalls {
			callID := call.ID
			if callID == "" {
				callID = newResponseID("call_")
			}
			output = append(output, map[string]any{
				"id": newResponseID("fc_"), "type": "function_call", "status": "completed",
				"call_id": callID, "name": call.Function.Name, "arguments": call.Function.Arguments,
			})
		}
	}
	_ = responseID
	return output
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func (g *Gateway) streamNativeResponse(writer http.ResponseWriter, ctx context.Context, route *route, provider llmprovider.ResponsesProvider, body []byte, headers http.Header, model string) {
	stream, err := provider.CreateResponseStream(ctx, body, headers)
	if err != nil {
		writeProviderError(writer, err)
		return
	}
	defer stream.Close()
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("streaming is unsupported by the HTTP server"))
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	if headerer, ok := stream.(llmprovider.ResponseHeaderer); ok {
		copySelectedHeaders(writer.Header(), headerer.ResponseHeaders(), route.forwardResponseHeaders)
	}
	writer.WriteHeader(http.StatusOK)
	for {
		event, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return
		}
		if recvErr != nil {
			writeSSEEvent(writer, "error", mustJSON(errorEnvelope(http.StatusBadGateway, recvErr)))
			flusher.Flush()
			return
		}
		writeSSEEvent(writer, event.Event, rewriteResponseEventModel(event.Data, model))
		flusher.Flush()
	}
}

func (g *Gateway) streamAdaptedResponse(writer http.ResponseWriter, ctx context.Context, route *route, model string, request responseRequest, chatRequest llmprovider.ChatRequest) {
	stream, err := route.provider.ChatStream(ctx, chatRequest)
	if err != nil {
		writeProviderError(writer, err)
		return
	}
	defer stream.Close()
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("streaming is unsupported by the HTTP server"))
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	if headerer, ok := stream.(llmprovider.ResponseHeaderer); ok {
		copySelectedHeaders(writer.Header(), headerer.ResponseHeaders(), route.forwardResponseHeaders)
	}
	writer.WriteHeader(http.StatusOK)
	responseID := newResponseID("resp_")
	itemID := newResponseID("msg_")
	sequence := 0
	emit := func(event string, value map[string]any) {
		value["type"] = event
		value["sequence_number"] = sequence
		sequence++
		writeSSEEvent(writer, event, mustJSON(value))
		flusher.Flush()
	}
	base := map[string]any{"id": responseID, "object": "response", "created_at": time.Now().Unix(), "status": "in_progress", "model": model, "output": []any{}}
	emit("response.created", map[string]any{"response": base})
	emit("response.in_progress", map[string]any{"response": base})
	var text strings.Builder
	var usage llmprovider.Usage
	started := false
	textOutputIndex := -1
	nextOutputIndex := 0
	toolCalls := make(map[int]llmprovider.ToolCall)
	toolOutputIndexes := make(map[int]int)
	toolItemIDs := make(map[int]string)
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			emit("error", map[string]any{"code": "upstream_error", "message": recvErr.Error()})
			return
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Delta == nil {
				continue
			}
			delta := choice.Delta.TextContent()
			if delta != "" {
				if !started {
					started = true
					textOutputIndex = nextOutputIndex
					nextOutputIndex++
					emit("response.output_item.added", map[string]any{"output_index": textOutputIndex, "item": map[string]any{"id": itemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}})
					emit("response.content_part.added", map[string]any{"item_id": itemID, "output_index": textOutputIndex, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
				}
				text.WriteString(delta)
				emit("response.output_text.delta", map[string]any{"item_id": itemID, "output_index": textOutputIndex, "content_index": 0, "delta": delta, "logprobs": []any{}})
			}
			for _, fragment := range choice.Delta.ToolCalls {
				current, known := toolCalls[fragment.Index]
				if fragment.ID != "" {
					current.ID = fragment.ID
				}
				if fragment.Type != "" {
					current.Type = fragment.Type
				}
				if fragment.Function.Name != "" {
					current.Function.Name += fragment.Function.Name
				}
				current.Function.Arguments += fragment.Function.Arguments
				toolCalls[fragment.Index] = current
				if !known {
					toolOutputIndexes[fragment.Index] = nextOutputIndex
					nextOutputIndex++
					toolItemIDs[fragment.Index] = newResponseID("fc_")
					callID := current.ID
					if callID == "" {
						callID = newResponseID("call_")
						current.ID = callID
						toolCalls[fragment.Index] = current
					}
					emit("response.output_item.added", map[string]any{"output_index": toolOutputIndexes[fragment.Index], "item": map[string]any{
						"id": toolItemIDs[fragment.Index], "type": "function_call", "status": "in_progress",
						"call_id": callID, "name": current.Function.Name, "arguments": "",
					}})
				}
				if fragment.Function.Arguments != "" {
					emit("response.function_call_arguments.delta", map[string]any{
						"item_id": toolItemIDs[fragment.Index], "output_index": toolOutputIndexes[fragment.Index], "delta": fragment.Function.Arguments,
					})
				}
			}
		}
	}
	if started {
		emit("response.output_text.done", map[string]any{"item_id": itemID, "output_index": textOutputIndex, "content_index": 0, "text": text.String(), "logprobs": []any{}})
		part := map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}
		emit("response.content_part.done", map[string]any{"item_id": itemID, "output_index": textOutputIndex, "content_index": 0, "part": part})
		emit("response.output_item.done", map[string]any{"output_index": textOutputIndex, "item": map[string]any{"id": itemID, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}})
	}
	output := make([]any, nextOutputIndex)
	if started {
		output[textOutputIndex] = map[string]any{"id": itemID, "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}}}
	}
	toolIndexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		toolIndexes = append(toolIndexes, index)
	}
	sort.Ints(toolIndexes)
	for _, index := range toolIndexes {
		call := toolCalls[index]
		if call.ID == "" {
			call.ID = newResponseID("call_")
		}
		outputIndex := toolOutputIndexes[index]
		item := map[string]any{"id": toolItemIDs[index], "type": "function_call", "status": "completed", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments}
		output[outputIndex] = item
		emit("response.function_call_arguments.done", map[string]any{"item_id": toolItemIDs[index], "output_index": outputIndex, "arguments": call.Function.Arguments})
		emit("response.output_item.done", map[string]any{"output_index": outputIndex, "item": item})
	}
	completed := map[string]any{"id": responseID, "object": "response", "created_at": base["created_at"], "status": "completed", "model": model, "output": output,
		"usage": map[string]any{"input_tokens": usage.PromptTokens, "output_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens}}
	_ = request
	emit("response.completed", map[string]any{"response": completed})
}

func writeSSEEvent(writer io.Writer, event string, data []byte) {
	if event != "" {
		_, _ = fmt.Fprintf(writer, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
