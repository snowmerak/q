package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	emptyChatResponseRetries = 2
	acpContentBlockMetadata  = "q_acp_content_block"
)

var errEmptyChatResponse = errors.New("provider returned an empty assistant response")

type chatRequestAttempt func(context.Context, client.ChatRequest) (*client.ChatResponse, error)

func chatWithEmptyResponseRecovery(
	ctx context.Context,
	configuredClient ModelClient,
	request client.ChatRequest,
) (*client.ChatResponse, error) {
	return recoverEmptyChatResponse(ctx, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		return chatWithConversationRecovery(ctx, configuredClient, request)
	})
}

func streamChatWithEmptyResponseRecovery(
	ctx context.Context,
	configuredClient ModelClient,
	request client.ChatRequest,
	emit func(StreamDelta) error,
) (*client.ChatResponse, error) {
	return recoverEmptyChatResponse(ctx, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		return streamChatWithConversationRecovery(ctx, configuredClient, request, emit)
	})
}

func recoverEmptyChatResponse(
	ctx context.Context,
	request client.ChatRequest,
	attempt chatRequestAttempt,
) (*client.ChatResponse, error) {
	attempts := 0
	for retry := 0; retry <= emptyChatResponseRetries; retry++ {
		response, err := attempt(ctx, request)
		if err != nil || !isEmptyChatResponse(response) {
			return response, err
		}
		attempts++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request.ConversationID = ""
	}

	recoveryRequest, removedToolHistory := withoutToolCallHistory(request)
	if removedToolHistory {
		response, err := attempt(ctx, recoveryRequest)
		if err != nil || !isEmptyChatResponse(response) {
			return response, err
		}
		attempts++
	}
	return nil, fmt.Errorf("%w after %d attempts", errEmptyChatResponse, attempts)
}

func isEmptyChatResponse(response *client.ChatResponse) bool {
	if response == nil || len(response.Choices) == 0 {
		return false
	}
	message := response.Choices[0].Message
	return strings.TrimSpace(message.TextContent()) == "" && len(message.ToolCalls) == 0
}

func withoutToolCallHistory(request client.ChatRequest) (client.ChatRequest, bool) {
	messages := make([]client.Message, 0, len(request.Messages))
	removed := false
	for _, message := range request.Messages {
		if message.Role == client.RoleTool {
			removed = true
			continue
		}
		if len(message.ToolCalls) > 0 {
			removed = true
			message.ToolCalls = nil
			if strings.TrimSpace(message.TextContent()) == "" {
				continue
			}
		}
		messages = append(messages, message)
	}
	request.Messages = messages
	request.ConversationID = ""
	return request, removed
}

func chatWithConversationRecovery(
	ctx context.Context,
	configuredClient ModelClient,
	request client.ChatRequest,
) (*client.ChatResponse, error) {
	response, err := configuredClient.Chat(ctx, request)
	if err == nil || request.ConversationID == "" || !isMissingCodexRollout(err) {
		return response, err
	}

	request.ConversationID = ""
	response, retryErr := configuredClient.Chat(ctx, request)
	if retryErr != nil {
		return nil, fmt.Errorf("codex conversation recovery after missing rollout: %w", retryErr)
	}
	return response, nil
}

func streamChatWithConversationRecovery(
	ctx context.Context,
	configuredClient ModelClient,
	request client.ChatRequest,
	emit func(StreamDelta) error,
) (*client.ChatResponse, error) {
	streaming, ok := configuredClient.(StreamingModelClient)
	if !ok {
		return chatWithConversationRecovery(ctx, configuredClient, request)
	}

	response, received, err := consumeChatStream(ctx, streaming, request, emit)
	if err == nil || request.ConversationID == "" || received || !isMissingCodexRollout(err) {
		return response, err
	}

	request.ConversationID = ""
	response, _, retryErr := consumeChatStream(ctx, streaming, request, emit)
	if retryErr != nil {
		return nil, fmt.Errorf("codex conversation recovery after missing rollout: %w", retryErr)
	}
	return response, nil
}

func consumeChatStream(
	ctx context.Context,
	configuredClient StreamingModelClient,
	request client.ChatRequest,
	emit func(StreamDelta) error,
) (*client.ChatResponse, bool, error) {
	request = withStreamUsage(request)
	stream, err := configuredClient.ChatStream(ctx, request)
	if err != nil {
		return nil, false, err
	}
	defer stream.Close()

	response := &client.ChatResponse{
		Object: "chat.completion",
		Model:  request.Model,
		Choices: []client.Choice{{
			Index: 0, Message: client.Message{Role: client.RoleAssistant},
		}},
	}
	received := false
	startedThinking := false
	startedResponse := false
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return response, received, nil
		}
		if recvErr != nil {
			return nil, received, recvErr
		}
		if chunk == nil {
			continue
		}
		received = true
		mergeChunkMetadata(response, chunk)
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				response.Choices[0].FinishReason = choice.FinishReason
			}
			if choice.Delta == nil {
				continue
			}
			delta := choice.Delta
			mergeToolCallFragments(&response.Choices[0].Message, delta.ToolCalls)

			thinking := ""
			content := delta.Content
			if choice.Phase == "commentary" {
				thinking, content = content, ""
			} else {
				thinking = rawThinkingDelta(chunk.Raw)
			}
			if thinking != "" {
				if emit != nil {
					if err := emit(StreamDelta{Kind: StreamThinking, Content: thinking, Start: !startedThinking}); err != nil {
						return nil, received, err
					}
				}
				startedThinking = true
			}
			if content != "" {
				response.Choices[0].Message.Content += content
				if emit != nil {
					if err := emit(StreamDelta{Kind: StreamResponse, Content: content, Start: !startedResponse}); err != nil {
						return nil, received, err
					}
				}
				startedResponse = true
			}
		}
	}
}

func withStreamUsage(request client.ChatRequest) client.ChatRequest {
	extra := make(map[string]any, len(request.Extra)+1)
	maps.Copy(extra, request.Extra)
	options := map[string]any{"include_usage": true}
	if configured, ok := extra["stream_options"].(map[string]any); ok {
		options = make(map[string]any, len(configured)+1)
		maps.Copy(options, configured)
		options["include_usage"] = true
	}
	extra["stream_options"] = options
	request.Extra = extra
	return request
}

func mergeChunkMetadata(response *client.ChatResponse, chunk *client.ChatChunk) {
	if response.ID == "" {
		response.ID = chunk.ID
	}
	if chunk.Model != "" {
		response.Model = chunk.Model
	}
	if chunk.ConversationID != "" {
		response.ConversationID = chunk.ConversationID
	}
	if chunk.Usage != nil {
		response.Usage = *chunk.Usage
	}
}

func mergeToolCallFragments(message *client.Message, fragments []client.ToolCall) {
	for _, fragment := range fragments {
		index := max(fragment.Index, 0)
		for len(message.ToolCalls) <= index {
			message.ToolCalls = append(message.ToolCalls, client.ToolCall{Index: len(message.ToolCalls)})
		}
		call := &message.ToolCalls[index]
		if fragment.ID != "" {
			call.ID = fragment.ID
		}
		if fragment.Type != "" {
			call.Type = fragment.Type
		}
		if fragment.Function.Name != "" {
			call.Function.Name += fragment.Function.Name
		}
		call.Function.Arguments += fragment.Function.Arguments
	}
}

func rawThinkingDelta(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var envelope struct {
		Choices []struct {
			Delta map[string]json.RawMessage `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Choices) == 0 {
		return ""
	}
	for _, field := range []string{"reasoning_content", "reasoning", "thinking"} {
		var value string
		if json.Unmarshal(envelope.Choices[0].Delta[field], &value) == nil && value != "" {
			return value
		}
	}
	return ""
}

func isMissingCodexRollout(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	if apiError, ok := errors.AsType[*client.APIError](err); ok {
		if apiError.StatusCode != 502 {
			return false
		}
		if apiError.Message != "" {
			message = apiError.Message
		}
	}
	message = strings.ToLower(message)
	return strings.Contains(message, "codex: json-rpc error -32600") &&
		strings.Contains(message, "no rollout found for thread id")
}

func providerMessages(messages []client.Message, coalesceInstructions bool) []client.Message {
	result := append([]client.Message(nil), messages...)
	for index := range result {
		result[index].ContentParts = providerSafeContentParts(result[index].ContentParts)
		if result[index].Role != client.RoleUser {
			result[index].Name = ""
		}
	}
	if !coalesceInstructions {
		return result
	}
	leading := 0
	var instructions []string
	for leading < len(result) && (result[leading].Role == client.RoleSystem || result[leading].Role == client.RoleDeveloper) {
		if content := strings.TrimSpace(result[leading].TextContent()); content != "" {
			instructions = append(instructions, content)
		}
		leading++
	}
	if leading == 0 {
		return result
	}
	merged := client.Message{Role: client.RoleSystem, Content: strings.Join(instructions, "\n\n")}
	coalesced := make([]client.Message, 0, len(result)-leading+1)
	coalesced = append(coalesced, merged)
	return append(coalesced, result[leading:]...)
}

func providerSafeContentParts(parts []client.MessageContentPart) []client.MessageContentPart {
	if len(parts) == 0 {
		return nil
	}
	result := make([]client.MessageContentPart, 0, len(parts))
	for _, part := range parts {
		clean := make(client.MessageContentPart, len(part))
		for key, value := range part {
			if key != acpContentBlockMetadata {
				clean[key] = value
			}
		}
		if len(clean) > 0 {
			result = append(result, clean)
		}
	}
	return result
}
