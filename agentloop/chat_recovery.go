package agentloop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

var errEmptyChatResponse = client.ErrEmptyResponse

// ErrEmptyChatResponse identifies a provider response that remained blank after recovery.
var ErrEmptyChatResponse = errEmptyChatResponse

// ChatWithEmptyResponseRecovery applies Q's bounded empty-response recovery.
func ChatWithEmptyResponseRecovery(ctx context.Context, configuredClient ChatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	return chatWithEmptyResponseRecovery(ctx, configuredClient, request)
}

// ChatWithConversationRecovery retries a missing Codex rollout with full history.
func ChatWithConversationRecovery(ctx context.Context, configuredClient ChatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	return chatWithEmptyResponseRecovery(ctx, configuredClient, request)
}

func chatWithEmptyResponseRecovery(ctx context.Context, configuredClient ChatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	ctx = client.WithUsageRole(ctx, "main")
	return client.RecoverGeneration(ctx, configuredClient, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		return chatWithConversationRecovery(ctx, configuredClient, request)
	})
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
