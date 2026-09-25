package app

import (
	"context"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

const (
	AgentStreamThinking = agentloop.AgentStreamThinking
	AgentStreamResponse = agentloop.AgentStreamResponse
	chatStreamThinking  = AgentStreamThinking
	chatStreamResponse  = AgentStreamResponse
)

type AgentStreamDelta = agentloop.AgentStreamDelta
type chatStreamDelta = AgentStreamDelta
type streamingChatClient = agentloop.StreamingChatClient

func streamChatWithEmptyResponseRecovery(ctx context.Context, configuredClient chatClient, request client.ChatRequest, emit func(chatStreamDelta) bool) (*client.ChatResponse, error) {
	return agentloop.StreamChatWithEmptyResponseRecovery(ctx, configuredClient, request, emit)
}

func consumeChatStream(ctx context.Context, configuredClient streamingChatClient, request client.ChatRequest, emit func(chatStreamDelta) bool) (*client.ChatResponse, bool, error) {
	return agentloop.ConsumeChatStream(ctx, configuredClient, request, emit)
}
