package app

import (
	"context"
	"errors"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type rolloutRecoveryClient struct {
	requests []client.ChatRequest
}

func (c *rolloutRecoveryClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	c.requests = append(c.requests, request)
	switch len(c.requests) {
	case 1:
		return chatResponse("stale-thread", "first reply"), nil
	case 2:
		return nil, &client.APIError{
			StatusCode: 502,
			Message:    "codex: JSON-RPC error -32600: no rollout found for thread id stale-thread",
		}
	default:
		return chatResponse("replacement-thread", "second reply"), nil
	}
}

func (c *rolloutRecoveryClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (c *rolloutRecoveryClient) Close() error                                       { return nil }

func TestChatRebuildsContextWhenCodexRolloutIsMissing(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "codex/test-model"
	configuredClient := &rolloutRecoveryClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, configuredClient)

	m = submitAndReceive(t, m, "first")
	m = submitAndReceive(t, m, "second")

	if len(configuredClient.requests) != 3 {
		t.Fatalf("requests = %#v", configuredClient.requests)
	}
	failedResume := configuredClient.requests[1]
	rebuilt := configuredClient.requests[2]
	if failedResume.ConversationID != "stale-thread" {
		t.Fatalf("failed resume conversation = %q", failedResume.ConversationID)
	}
	if rebuilt.ConversationID != "" {
		t.Fatalf("rebuilt conversation = %q, want empty", rebuilt.ConversationID)
	}
	if len(rebuilt.Messages) != len(failedResume.Messages) {
		t.Fatalf("rebuilt messages = %d, want %d", len(rebuilt.Messages), len(failedResume.Messages))
	}
	if m.conversationID != "replacement-thread" || m.messages[len(m.messages)-1].Content != "second reply" {
		t.Fatalf("recovered model conversation = %q, messages = %#v", m.conversationID, m.messages)
	}
}

func TestChatDoesNotRetryOtherFailures(t *testing.T) {
	errUnavailable := &client.APIError{StatusCode: 502, Message: "codex: temporarily unavailable"}
	configuredClient := &singleErrorClient{err: errUnavailable}
	_, err := chatWithConversationRecovery(context.Background(), configuredClient, client.ChatRequest{
		ConversationID: "thread", Messages: []client.Message{{Role: client.RoleUser, Content: "hello"}},
	})
	if !errors.Is(err, errUnavailable) || configuredClient.calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, configuredClient.calls)
	}
}

type singleErrorClient struct {
	err   error
	calls int
}

func (c *singleErrorClient) Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
	c.calls++
	return nil, c.err
}

func (c *singleErrorClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (c *singleErrorClient) Close() error                                       { return nil }

func chatResponse(conversationID, content string) *client.ChatResponse {
	return &client.ChatResponse{
		ConversationID: conversationID,
		Choices:        []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: content}}},
	}
}
