package app

import (
	"context"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

var errEmptyChatResponse = agentloop.ErrEmptyChatResponse

func chatWithEmptyResponseRecovery(ctx context.Context, configuredClient chatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	return agentloop.ChatWithEmptyResponseRecovery(ctx, configuredClient, request)
}

func chatWithConversationRecovery(ctx context.Context, configuredClient chatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	return agentloop.ChatWithConversationRecovery(ctx, configuredClient, request)
}
