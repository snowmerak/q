package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
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
	if !capabilities.LoadSession || capabilities.SessionCapabilities.Resume == nil || capabilities.SessionCapabilities.List == nil {
		t.Fatalf("unexpected single-session compatibility capabilities: %#v", capabilities)
	}
	if capabilities.SessionCapabilities.Close == nil || capabilities.SessionCapabilities.Delete == nil || !capabilities.McpCapabilities.Http {
		t.Fatal("session close/delete or MCP HTTP capability was not advertised")
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

func TestACPAgentLoadsAndResumesTheWorkspaceSession(t *testing.T) {
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("persist me")},
	}); err != nil {
		t.Fatal(err)
	}

	restartedState := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	restartedState.workspaceStore = &workspaceStore
	restartedState.toolRuntime = &fakeAgentTools{}
	value := config.Default()
	value.Provider.Model = "test-model"
	restartedState.enterChat(value, &fakeClient{})
	restartedConnection := &fakeACPConnection{}
	restarted := newACPAgent(&restartedState, workspaceStore.Root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restarted.setConnection(restartedConnection)
	if restarted.sessionID != sessionID {
		t.Fatalf("restarted session ID = %q, want %q", restarted.sessionID, sessionID)
	}
	if _, err := restarted.LoadSession(t.Context(), acp.LoadSessionRequest{
		SessionId:  sessionID,
		Cwd:        workspaceStore.Root,
		McpServers: []acp.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}

	var userText, assistantText string
	for _, notification := range restartedConnection.snapshot() {
		if chunk := notification.Update.UserMessageChunk; chunk != nil && chunk.Content.Text != nil {
			userText += chunk.Content.Text.Text
		}
		if chunk := notification.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			assistantText += chunk.Content.Text.Text
		}
	}
	if userText != "persist me" || assistantText != "reply 1" {
		t.Fatalf("loaded transcript: user=%q assistant=%q", userText, assistantText)
	}

	if _, err := restarted.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	updatesBeforeResume := len(restartedConnection.snapshot())
	if _, err := restarted.ResumeSession(t.Context(), acp.ResumeSessionRequest{
		SessionId: sessionID,
		Cwd:       workspaceStore.Root,
	}); err != nil {
		t.Fatal(err)
	}
	if updatesAfterResume := len(restartedConnection.snapshot()); updatesAfterResume != updatesBeforeResume {
		t.Fatalf("resume replayed history: updates before=%d after=%d", updatesBeforeResume, updatesAfterResume)
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
	if saved.Title != "hello from ACP" || saved.UpdatedAt == nil || saved.UpdatedAt.IsZero() {
		t.Fatalf("persisted session metadata = title %q updated %v", saved.Title, saved.UpdatedAt)
	}

	listed, err := agent.ListSessions(t.Context(), acp.ListSessionsRequest{Cwd: acp.Ptr(workspaceStore.Root)})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionId != sessionID || listed.Sessions[0].Title == nil ||
		*listed.Sessions[0].Title != "hello from ACP" || listed.Sessions[0].UpdatedAt == nil {
		t.Fatalf("listed sessions = %#v", listed.Sessions)
	}
	otherRoot := t.TempDir()
	filtered, err := agent.ListSessions(t.Context(), acp.ListSessionsRequest{Cwd: &otherRoot})
	if err != nil || len(filtered.Sessions) != 0 {
		t.Fatalf("filtered sessions = %#v, err = %v", filtered.Sessions, err)
	}

	if _, err := agent.UnstableDeleteSession(t.Context(), acp.UnstableDeleteSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	listed, err = agent.ListSessions(t.Context(), acp.ListSessionsRequest{})
	if err != nil || len(listed.Sessions) != 0 {
		t.Fatalf("sessions after delete = %#v, err = %v", listed.Sessions, err)
	}
	newSessionID := openTestACPSession(t, agent, workspaceStore.Root)
	if newSessionID == sessionID {
		t.Fatalf("session/delete reused deleted ID %q", sessionID)
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
	var planStatuses []acp.PlanEntryStatus
	wantPath := filepath.Join(workspaceStore.Root, "main.go")
	for _, notification := range connection.snapshot() {
		if update := notification.Update.ToolCall; update != nil && update.ToolCallId == "call-1" {
			started = update.Kind == acp.ToolKindEdit && update.Status == acp.ToolCallStatusInProgress &&
				len(update.Locations) == 1 && update.Locations[0].Path == wantPath
		}
		if update := notification.Update.ToolCallUpdate; update != nil && update.ToolCallId == "call-1" {
			completed = update.Status != nil && *update.Status == acp.ToolCallStatusCompleted && len(update.Content) == 1
		}
		if update := notification.Update.Plan; update != nil && len(update.Entries) == 1 && update.Entries[0].Content == "Create a Go project" {
			planStatuses = append(planStatuses, update.Entries[0].Status)
		}
	}
	if !started || !completed || len(planStatuses) != 2 || planStatuses[0] != acp.PlanEntryStatusInProgress ||
		planStatuses[1] != acp.PlanEntryStatusCompleted {
		t.Fatalf("write_file ACP lifecycle: started=%v completed=%v plans=%v, updates=%#v", started, completed, planStatuses, connection.snapshot())
	}
}

func TestACPAgentAdvertisesAndHandlesHeadlessCommands(t *testing.T) {
	configuredClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/help")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 0 {
		t.Fatalf("command response = %#v, model requests = %d", response, len(configuredClient.requests))
	}
	var commands []acp.AvailableCommand
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AvailableCommandsUpdate; update != nil {
			commands = update.AvailableCommands
		}
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if len(commands) != 2 || commands[0].Name != "learn" || !strings.Contains(output, "/learn") {
		t.Fatalf("commands = %#v, output = %q", commands, output)
	}
}

func TestACPAgentAcceptsImagesForCompatibleRoutes(t *testing.T) {
	configuredClient := &fakeClient{}
	agent, workspaceStore, _ := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.Model = "openai/test-model"
	agent.state.gatewayConfig = gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "openai", Prefix: "openai", Type: "openai-compatible", Enabled: true,
	}}}
	initialized, err := agent.Initialize(t.Context(), acp.InitializeRequest{})
	if err != nil || !initialized.AgentCapabilities.PromptCapabilities.Image {
		t.Fatalf("image capability = %#v, err = %v", initialized.AgentCapabilities.PromptCapabilities, err)
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	imageData := "iVBORw0KGgo="
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt: []acp.ContentBlock{
			acp.TextBlock("inspect this"),
			acp.ImageBlock(imageData, "image/png"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(configuredClient.requests) != 1 {
		t.Fatalf("model requests = %d", len(configuredClient.requests))
	}
	var prompt client.Message
	for _, message := range configuredClient.requests[0].Messages {
		if message.Role == client.RoleUser {
			prompt = message
		}
	}
	if len(prompt.ContentParts) != 2 || prompt.ContentParts[1]["type"] != "image_url" {
		t.Fatalf("image prompt = %#v", prompt)
	}
}

func TestMergeACPMCPServersUsesEphemeralCredentials(t *testing.T) {
	base := mcpconfig.Default()
	base.Servers["workspace"] = mcpconfig.ServerConfig{Transport: mcpconfig.TransportStdio, Command: "workspace-mcp"}
	base.Roles[mcpconfig.RoleDefault] = []string{"workspace"}
	merged, sessionIDs, err := mergeACPMCPServers(base, []acp.McpServer{{
		Stdio: &acp.McpServerStdio{
			Name: "client", Command: "client-mcp", Args: []string{"serve"},
			Env: []acp.EnvVariable{{Name: "TOKEN", Value: "secret-value"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessionIDs) != 1 {
		t.Fatalf("session MCP IDs = %#v", sessionIDs)
	}
	var sessionID string
	for id := range sessionIDs {
		sessionID = id
	}
	if merged.Servers[sessionID].ResolvedEnv["TOKEN"] != "secret-value" {
		t.Fatalf("merged ACP MCP server = %#v", merged.Servers[sessionID])
	}
	for _, role := range mcpconfig.RoleIDs() {
		if !slices.Contains(merged.Roles[role], sessionID) {
			t.Fatalf("role %q did not receive ACP MCP server: %#v", role, merged.Roles[role])
		}
	}
	body, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-value") {
		t.Fatalf("ephemeral MCP secret was serialized: %s", body)
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
