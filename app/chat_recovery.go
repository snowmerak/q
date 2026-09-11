package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

const emptyChatResponseRetries = 2

var errEmptyChatResponse = errors.New("provider returned an empty assistant response")

type chatRequestAttempt func(context.Context, client.ChatRequest) (*client.ChatResponse, error)

// chatWithEmptyResponseRecovery retries a blank completion on a fresh
// provider conversation. If bounded identical retries remain blank, it makes
// one final request without prior tool-call exchanges, which are often the
// largest and least reusable part of a long context.
func chatWithEmptyResponseRecovery(
	ctx context.Context,
	configuredClient chatClient,
	request client.ChatRequest,
) (*client.ChatResponse, error) {
	return recoverEmptyChatResponse(ctx, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		return chatWithConversationRecovery(ctx, configuredClient, request)
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

// chatWithConversationRecovery retries one request on a fresh Codex thread
// when an explicitly resumed thread no longer has a rollout. The complete
// caller-supplied message history lets the provider rebuild equivalent context.
func chatWithConversationRecovery(
	ctx context.Context,
	configuredClient chatClient,
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

func isMissingCodexRollout(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	var apiError *client.APIError
	if errors.As(err, &apiError) {
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
