package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

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
