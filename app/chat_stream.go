package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	chatStreamThinking = "thinking"
	chatStreamResponse = "response"
)

type chatStreamDelta struct {
	Kind    string
	Content string
	Start   bool
}

type streamingChatClient interface {
	ChatStream(context.Context, client.ChatRequest) (client.Stream, error)
}

// streamChatWithConversationRecovery consumes provider deltas while retaining
// the complete ChatResponse shape expected by the existing agent loop. Clients
// without streaming support keep the previous one-shot behavior.
func streamChatWithConversationRecovery(
	ctx context.Context,
	configuredClient chatClient,
	request client.ChatRequest,
	emit func(chatStreamDelta) bool,
) (*client.ChatResponse, error) {
	streaming, ok := configuredClient.(streamingChatClient)
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
	configuredClient streamingChatClient,
	request client.ChatRequest,
	emit func(chatStreamDelta) bool,
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
				if emit != nil && !emit(chatStreamDelta{Kind: chatStreamThinking, Content: thinking, Start: !startedThinking}) {
					return nil, received, ctx.Err()
				}
				startedThinking = true
			}
			if content != "" {
				response.Choices[0].Message.Content += content
				if emit != nil && !emit(chatStreamDelta{Kind: chatStreamResponse, Content: content, Start: !startedResponse}) {
					return nil, received, ctx.Err()
				}
				startedResponse = true
			}
		}
	}
}

func withStreamUsage(request client.ChatRequest) client.ChatRequest {
	extra := make(map[string]any, len(request.Extra)+1)
	for key, value := range request.Extra {
		extra[key] = value
	}
	options := map[string]any{"include_usage": true}
	if configured, ok := extra["stream_options"].(map[string]any); ok {
		options = make(map[string]any, len(configured)+1)
		for key, value := range configured {
			options[key] = value
		}
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
		index := fragment.Index
		if index < 0 {
			index = 0
		}
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

// OpenAI-compatible reasoning servers commonly place streamed reasoning in
// delta.reasoning_content, delta.reasoning, or delta.thinking. These extension
// fields are intentionally recovered from Raw without changing the shared wire
// types.
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
		if json.Unmarshal(envelope.Choices[0].Delta[field], &value) == nil && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
