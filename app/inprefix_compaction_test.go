package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/workspace"
)

type inPrefixFakeClient struct {
	requests []client.ChatRequest
	chat     func(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

func (c *inPrefixFakeClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	request.Tools = append([]client.Tool(nil), request.Tools...)
	c.requests = append(c.requests, request)
	return c.chat(ctx, request)
}

func (c *inPrefixFakeClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (c *inPrefixFakeClient) Close() error                                       { return nil }

func TestAppCompactionUsesInPrefixWithToolsAndConversationIDAndFallsBack(t *testing.T) {
	for _, mode := range []string{"in_prefix", "fallback_tool_call", "fallback_invalid_json"} {
		t.Run(mode, func(t *testing.T) {
			fake := &inPrefixFakeClient{}
			fake.chat = func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
				if memory.ClassifyCheckpointRequest(request) == memory.CheckpointDetached {
					return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
						Role: client.RoleAssistant, Content: testCheckpointJSON("detached fallback checkpoint"),
					}}}}, nil
				}
				if memory.ClassifyCheckpointRequest(request) != memory.CheckpointNone {
					switch mode {
					case "fallback_tool_call":
						return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
							Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
								ID: "call-1", Type: client.ToolTypeFunction,
								Function: client.FunctionCall{Name: "read_file", Arguments: `{}`},
							}},
						}}}}, nil
					case "fallback_invalid_json":
						return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
							Role: client.RoleAssistant, Content: "not valid checkpoint json",
						}}}}, nil
					default:
						return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
							Role: client.RoleAssistant, Content: testCheckpointJSON("in-prefix checkpoint"),
						}}}}, nil
					}
				}
				return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
					Role: client.RoleAssistant, Content: "normal reply",
				}}}}, nil
			}

			value := config.Default()
			value.Provider.Model = "test-model"
			value.Provider.ContextWindow = 16_000
			m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
			store := workspace.Store{Root: t.TempDir()}
			m.workspaceStore = &store
			m.toolRuntime = largeResultRuntime{content: "ok"}
			m.enterChat(value, fake)
			m.messages = []client.Message{
				{Role: client.RoleSystem, Content: "Keep the contract."},
				{Role: client.RoleAssistant, Content: strings.Repeat("prior ", 4_500)},
				{Role: client.RoleUser, Content: "latest request"},
			}
			m.memory = memory.New(memoryPolicy(value), m.messages)
			m.conversationID = "live-conv-123"
			if m.memory.ShouldCompact() {
				t.Fatal("test fixture must start below automatic compaction trigger")
			}

			m.input.SetValue("/compact")
			updated, command := m.submitChat()
			m = updated.(model)
			batch, ok := command().(tea.BatchMsg)
			if !ok {
				t.Fatal("expected compaction batch")
			}
			var result compactionResultMsg
			for _, child := range batch {
				if msg, ok := child().(compactionResultMsg); ok {
					result = msg
				}
			}
			updated, _ = m.Update(result)
			m = updated.(model)
			if m.turnErr != nil {
				t.Fatalf("unexpected compaction error: %v", m.turnErr)
			}
			if m.conversationID != "" {
				t.Fatalf("conversationID was not reset after compaction: %q", m.conversationID)
			}

			if mode == "in_prefix" {
				if len(fake.requests) != 1 {
					t.Fatalf("requests = %d, want 1", len(fake.requests))
				}
				req := fake.requests[0]
				if req.ConversationID != "live-conv-123" || len(req.Tools) == 0 || memory.ClassifyCheckpointRequest(req) != memory.CheckpointInPrefix {
					t.Fatalf("expected in-prefix request with conversationID and tools, got %#v", req)
				}
			} else {
				if len(fake.requests) != 2 {
					t.Fatalf("requests = %d, want 2 (in-prefix + detached)", len(fake.requests))
				}
				if memory.ClassifyCheckpointRequest(fake.requests[0]) != memory.CheckpointInPrefix || memory.ClassifyCheckpointRequest(fake.requests[1]) != memory.CheckpointDetached {
					t.Fatalf("expected in-prefix then detached fallback, got %#v", fake.requests)
				}
			}
		})
	}
}

func TestAppPreAppendUsesInPrefixBeforeOverflow(t *testing.T) {
	fake := &fakeClient{}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 16_000
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	store := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &store
	m.toolRuntime = largeResultRuntime{content: "ok"}
	m.enterChat(value, fake)
	history := []client.Message{
		{Role: client.RoleSystem, Content: "Keep the contract."},
		{Role: client.RoleAssistant, Content: strings.Repeat("x", 36_000)},
	}
	m.memory = memory.New(memoryPolicy(value), history)
	m.messages = append([]client.Message(nil), history...)
	m.conversationID = "old-conversation"

	incoming := "NEW_REQUEST:" + strings.Repeat("y", 6_000)
	if m.memory.ShouldCompact() || !m.memory.ShouldCompactAfterAppend(client.Message{Role: client.RoleUser, Content: incoming}) {
		t.Fatal("fixture must cross trigger only after incoming message")
	}
	updated, command := m.startChatTurn(incoming, true)
	m = updated.(model)
	batch := command().(tea.BatchMsg)
	var result compactionResultMsg
	for _, child := range batch {
		if msg, ok := child().(compactionResultMsg); ok {
			result = msg
		}
	}
	if result.err != nil && !errors.Is(result.err, nil) {
		t.Fatal(result.err)
	}
	if len(fake.requests) != 1 || fake.requests[0].ConversationID != "old-conversation" || len(fake.requests[0].Tools) == 0 || memory.ClassifyCheckpointRequest(fake.requests[0]) != memory.CheckpointInPrefix {
		t.Fatalf("expected in-prefix pre-append request, got %#v", fake.requests)
	}
	for _, msg := range fake.requests[0].Messages {
		if strings.Contains(msg.Content, "NEW_REQUEST:") {
			t.Fatal("incoming message leaked into pre-append compaction request")
		}
	}
}
