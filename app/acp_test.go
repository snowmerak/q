package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

type fakeACPConnection struct {
	mu      sync.Mutex
	updates []acp.SessionNotification
	answer  string
}

type blockingACPClient struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingACPClient) Chat(ctx context.Context, _ client.ChatRequest) (*client.ChatResponse, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingACPClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (b *blockingACPClient) Close() error                                       { return nil }

func (f *fakeACPConnection) SessionUpdate(_ context.Context, notification acp.SessionNotification) error {
	f.mu.Lock()
	f.updates = append(f.updates, notification)
	f.mu.Unlock()
	return nil
}

func (f *fakeACPConnection) UnstableCreateElicitation(
	context.Context,
	acp.UnstableCreateElicitationRequest,
) (acp.UnstableCreateElicitationResponse, error) {
	return acp.UnstableCreateElicitationResponse{
		Accept: &acp.UnstableCreateElicitationAccept{
			Action: "accept",
			Content: map[string]any{
				"answer": f.answer,
			},
		},
	}, nil
}

func (f *fakeACPConnection) snapshot() []acp.SessionNotification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acp.SessionNotification(nil), f.updates...)
}

func testACPAgent(t *testing.T, configuredClient chatClient, tools agentToolRuntime) (*acpAgent, workspace.Store, *fakeACPConnection) {
	t.Helper()
	root := t.TempDir()
	workspaceStore := workspace.Store{Root: root}
	state := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	state.workspaceStore = &workspaceStore
	state.toolRuntime = tools
	value := config.Default()
	value.Provider.Model = "test-model"
	state.enterChat(value, configuredClient)

	connection := &fakeACPConnection{}
	agent := newACPAgent(&state, root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	agent.setConnection(connection)
	return agent, workspaceStore, connection
}

func openTestACPSession(t *testing.T, agent *acpAgent, root string) acp.SessionId {
	t.Helper()
	response, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd:        root,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response.SessionId
}

func TestACPAgentKeepsOneSessionPerWorkspace(t *testing.T) {
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	initialized, err := agent.Initialize(t.Context(), acp.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := initialized.AgentCapabilities
	if capabilities.LoadSession || capabilities.SessionCapabilities.List != nil || capabilities.SessionCapabilities.Resume != nil {
		t.Fatalf("unexpected durable or multi-session capabilities: %#v", capabilities)
	}
	if capabilities.SessionCapabilities.Close == nil {
		t.Fatal("session/close capability was not advertised")
	}

	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	if _, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd: workspaceStore.Root, McpServers: []acp.McpServer{},
	}); err == nil {
		t.Fatal("second ACP session for the same workspace was accepted")
	}
	if _, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd: t.TempDir(), McpServers: []acp.McpServer{},
	}); err == nil {
		t.Fatal("session for a different workspace was accepted")
	}

	if _, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	reopened := openTestACPSession(t, agent, workspaceStore.Root)
	if reopened != sessionID {
		t.Fatalf("reopened session ID = %q, want %q", reopened, sessionID)
	}
}

func TestACPAgentPromptStreamsAndPersistsWorkspaceSession(t *testing.T) {
	agent, workspaceStore, connection := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	messageID := "67a90de8-1f34-4f1f-9984-6ad8ff17c455"
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		MessageId: &messageID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello from ACP")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || response.UserMessageId == nil || *response.UserMessageId != messageID {
		t.Fatalf("prompt response = %#v", response)
	}

	var streamed string
	for _, notification := range connection.snapshot() {
		if notification.SessionId != sessionID {
			t.Fatalf("notification session = %q, want %q", notification.SessionId, sessionID)
		}
		if chunk := notification.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			streamed += chunk.Content.Text.Text
		}
	}
	if streamed != "reply 1" {
		t.Fatalf("streamed response = %q", streamed)
	}

	saved, err := workspaceStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Transcript) != 2 || saved.Transcript[0].Role != client.RoleUser || saved.Transcript[0].Content != "hello from ACP" ||
		saved.Transcript[1].Role != client.RoleAssistant || saved.Transcript[1].Content != "reply 1" {
		t.Fatalf("persisted transcript = %#v", saved.Transcript)
	}
}

func TestACPAgentReportsToolLifecycle(t *testing.T) {
	tools := &fakeAgentTools{}
	agent, workspaceStore, connection := testACPAgent(t, &toolCallingClient{}, tools)
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("create main.go")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q", response.StopReason)
	}
	if len(tools.calls) != 1 || tools.calls[0].Function.Name != "write_file" {
		t.Fatalf("tool calls = %#v", tools.calls)
	}

	var started, completed bool
	wantPath := filepath.Join(workspaceStore.Root, "main.go")
	for _, notification := range connection.snapshot() {
		if update := notification.Update.ToolCall; update != nil && update.ToolCallId == "call-1" {
			started = update.Kind == acp.ToolKindEdit && update.Status == acp.ToolCallStatusInProgress &&
				len(update.Locations) == 1 && update.Locations[0].Path == wantPath
		}
		if update := notification.Update.ToolCallUpdate; update != nil && update.ToolCallId == "call-1" {
			completed = update.Status != nil && *update.Status == acp.ToolCallStatusCompleted && len(update.Content) == 1
		}
	}
	if !started || !completed {
		t.Fatalf("write_file ACP lifecycle: started=%v completed=%v, updates=%#v", started, completed, connection.snapshot())
	}
}

func TestACPAgentCancelsActivePrompt(t *testing.T) {
	configuredClient := &blockingACPClient{started: make(chan struct{})}
	agent, workspaceStore, _ := testACPAgent(t, configuredClient, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)

	responses := make(chan acp.PromptResponse, 1)
	errors := make(chan error, 1)
	go func() {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock("wait")},
		})
		responses <- response
		errors <- err
	}()

	select {
	case <-configuredClient.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model request did not start")
	}
	if err := agent.Cancel(t.Context(), acp.CancelNotification{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled prompt did not return")
	}
	response := <-responses
	if response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("stop reason = %q", response.StopReason)
	}

	saved, err := workspaceStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Transcript) != 1 || saved.Transcript[0].Role != client.RoleUser {
		t.Fatalf("cancelled transcript = %#v", saved.Transcript)
	}
}

func TestACPAgentUsesClientElicitationForQuestions(t *testing.T) {
	configuredClient := &askingClient{}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	connection.answer = "green"
	if _, err := agent.Initialize(t.Context(), acp.InitializeRequest{
		ClientCapabilities: acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("choose a color")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 2 {
		t.Fatalf("response = %#v, requests = %d", response, len(configuredClient.requests))
	}
	var answer string
	for _, message := range configuredClient.requests[1].Messages {
		if message.Role == client.RoleTool && message.ToolCallID == "ask-1" {
			answer = message.Content
		}
	}
	if !strings.Contains(answer, `"selected_choice_id":"green"`) {
		t.Fatalf("ask_to_user result = %q", answer)
	}
}
