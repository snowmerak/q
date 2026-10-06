package agentloop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

const emptyChatResponseRetries = 2

var errEmptyChatResponse = errors.New("provider returned an empty assistant response")

// ErrEmptyChatResponse identifies a provider response that remained blank after recovery.
var ErrEmptyChatResponse = errEmptyChatResponse

// ChatWithEmptyResponseRecovery applies Q's bounded empty-response recovery.
func ChatWithEmptyResponseRecovery(ctx context.Context, configuredClient ChatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	return chatWithEmptyResponseRecovery(ctx, configuredClient, request)
}

// ChatWithConversationRecovery retries a missing Codex rollout with full history.
func ChatWithConversationRecovery(ctx context.Context, configuredClient ChatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	return chatWithConversationRecovery(ctx, configuredClient, request)
}

type chatRequestAttempt func(context.Context, client.ChatRequest) (*client.ChatResponse, error)

// chatWithEmptyResponseRecovery retries a blank completion on a fresh
// provider conversation. If bounded identical retries remain blank, it makes
// one final request without prior tool-call exchanges, which are often the
// largest and least reusable part of a long context.
func chatWithEmptyResponseRecovery(
	ctx context.Context,
	configuredClient ChatClient,
	request client.ChatRequest,
) (*client.ChatResponse, error) {
	ctx = client.WithUsageRole(ctx, "main")
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
	var lastResponse *client.ChatResponse
	for retry := 0; retry <= emptyChatResponseRetries; retry++ {
		response, err := attempt(ctx, request)
		if err != nil || !isEmptyChatResponse(response) {
			return response, err
		}
		lastResponse = response
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
		lastResponse = response
		attempts++
	}
	return nil, fmt.Errorf("%w after %d attempts%s", errEmptyChatResponse, attempts, emptyResponseDiagnostic(lastResponse))
}

func emptyResponseDiagnostic(response *client.ChatResponse) string {
	if response == nil {
		return ""
	}
	var details []string
	if len(response.Choices) > 0 && response.Choices[0].FinishReason != "" {
		details = append(details, "finish_reason="+response.Choices[0].FinishReason)
	}
	if response.ID != "" {
		id := response.ID
		if len(id) > 128 {
			id = id[:128] + "…"
		}
		details = append(details, "response_id="+id)
	}
	if response.Usage.PromptTokens > 0 {
		details = append(details, fmt.Sprintf("prompt_tokens=%d", response.Usage.PromptTokens))
	}
	if response.Usage.CompletionTokens > 0 {
		details = append(details, fmt.Sprintf("completion_tokens=%d", response.Usage.CompletionTokens))
	}
	if len(details) == 0 {
		return ""
	}
	return " (" + strings.Join(details, ", ") + ")"
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
	configuredClient ChatClient,
	request client.ChatRequest,
) (*client.ChatResponse, error) {
	ctx = client.WithUsageRole(ctx, "main")
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
