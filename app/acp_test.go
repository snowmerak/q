package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/commitagent"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type fakeACPConnection struct {
	mu                  sync.Mutex
	updates             []acp.SessionNotification
	elicitationRequests []acp.UnstableCreateElicitationRequest
	answer              string
	elicitationAction   string
	elicitationErr      error
	elicitationContents []map[string]any
}

type fakeACPExternalTools struct {
	fakeAgentTools
	configured   []mcpconfig.Config
	cancelFirst  context.CancelFunc
	blockCall    int
	blockEntered chan struct{}
	blockRelease chan struct{}
}

func (f *fakeACPExternalTools) ConfigureExternal(_ context.Context, _ string, value mcpconfig.Config) []qtools.ExternalStatus {
	f.configured = append(f.configured, cloneMCPConfig(value))
	call := len(f.configured)
	if call == 1 && f.cancelFirst != nil {
		f.cancelFirst()
	}
	if call == f.blockCall {
		close(f.blockEntered)
		<-f.blockRelease
	}
	return nil
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
	_ context.Context,
	request acp.UnstableCreateElicitationRequest,
) (acp.UnstableCreateElicitationResponse, error) {
	f.mu.Lock()
	f.elicitationRequests = append(f.elicitationRequests, request)
	action := f.elicitationAction
	answer := f.answer
	err := f.elicitationErr
	var content map[string]any
	if len(f.elicitationContents) > 0 {
		content = f.elicitationContents[0]
		f.elicitationContents = f.elicitationContents[1:]
	}
	f.mu.Unlock()
	if err != nil {
		return acp.UnstableCreateElicitationResponse{}, err
	}
	switch action {
	case "decline":
		return acp.UnstableCreateElicitationResponse{
			Decline: &acp.UnstableCreateElicitationDecline{Action: "decline"},
		}, nil
	case "cancel":
		return acp.UnstableCreateElicitationResponse{
			Cancel: &acp.UnstableCreateElicitationCancel{Action: "cancel"},
		}, nil
	}
	if content == nil {
		content = map[string]any{"answer": answer}
	}
	return acp.UnstableCreateElicitationResponse{
		Accept: &acp.UnstableCreateElicitationAccept{
			Action:  "accept",
			Content: content,
		},
	}, nil
}

func (f *fakeACPConnection) snapshot() []acp.SessionNotification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acp.SessionNotification(nil), f.updates...)
}

func (f *fakeACPConnection) elicitationSnapshot() []acp.UnstableCreateElicitationRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acp.UnstableCreateElicitationRequest(nil), f.elicitationRequests...)
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
	t.Cleanup(func() {
		_ = agent.shutdown()
	})
	return agent, workspaceStore, connection
}

func activeACPWorkspaceStore(t *testing.T, agent *acpAgent) workspace.Store {
	t.Helper()
	if agent == nil || agent.state == nil || agent.state.workspaceStore == nil || agent.state.workspaceStore.SessionID == "" {
		t.Fatal("ACP agent has no active session store")
	}
	return *agent.state.workspaceStore
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

func TestACPAgentKeepsOneActiveSessionPerProcess(t *testing.T) {
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
	if reopened == sessionID {
		t.Fatalf("new session reused closed session ID %q", sessionID)
	}
	listed, err := agent.ListSessions(t.Context(), acp.ListSessionsRequest{Cwd: &workspaceStore.Root})
	if err != nil {
		t.Fatal(err)
	}
	listedIDs := make([]acp.SessionId, 0, len(listed.Sessions))
	for _, session := range listed.Sessions {
		listedIDs = append(listedIDs, session.SessionId)
	}
	if len(listedIDs) != 2 || !slices.Contains(listedIDs, sessionID) || !slices.Contains(listedIDs, reopened) {
		t.Fatalf("listed session IDs = %#v", listedIDs)
	}
	if _, err := agent.UnstableDeleteSession(t.Context(), acp.UnstableDeleteSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	listed, err = agent.ListSessions(t.Context(), acp.ListSessionsRequest{})
	if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].SessionId != reopened {
		t.Fatalf("sessions after deleting inactive session = %#v, %v", listed.Sessions, err)
	}
}

func TestACPAgentRejectsNewLifecycleWorkAfterShutdown(t *testing.T) {
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	if err := agent.shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewSession(t.Context(), acp.NewSessionRequest{Cwd: workspaceStore.Root}); err == nil {
		t.Fatal("session/new succeeded after shutdown")
	}
	if _, err := agent.ResumeSession(t.Context(), acp.ResumeSessionRequest{
		SessionId: sessionID, Cwd: workspaceStore.Root,
	}); err == nil {
		t.Fatal("session/resume succeeded after shutdown")
	}
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("late")},
	}); err == nil {
		t.Fatal("session/prompt succeeded after shutdown")
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
	if _, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	if agent.state.memory != nil || agent.state.learning != nil || len(agent.state.messages) != 0 ||
		agent.state.activeTask != nil || agent.state.workspaceStore == nil || agent.state.workspaceStore.SessionID != "" {
		t.Fatalf("closed ACP session retained in-memory state: %#v", agent.state.workspaceStore)
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
	if restarted.sessionID != "" {
		t.Fatalf("restarted agent selected a session before load: %q", restarted.sessionID)
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
	if _, err := restarted.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
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

	saved, err := activeACPWorkspaceStore(t, agent).Load()
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
	commandNames := make([]string, 0, len(commands))
	for _, command := range commands {
		commandNames = append(commandNames, command.Name)
	}
	if !slices.Equal(commandNames, []string{"plan", "agent:search", "commit", "learn", "clear", "help"}) ||
		!strings.Contains(output, "/plan") || !strings.Contains(output, "/agent:search") ||
		!strings.Contains(output, "/commit") || !strings.Contains(output, "/learn") || !strings.Contains(output, "/clear") {
		t.Fatalf("commands = %#v, output = %q", commands, output)
	}
}

type fakeACPCommitSession struct {
	proposals   [][]commitagent.Proposal
	proposal    int
	autoStaged  bool
	regenerated int
	committed   int
	pushed      int
	closed      int
	pushErr     error
}

func (f *fakeACPCommitSession) Proposals() []commitagent.Proposal {
	if len(f.proposals) == 0 {
		return nil
	}
	return append([]commitagent.Proposal(nil), f.proposals[min(f.proposal, len(f.proposals)-1)]...)
}
func (f *fakeACPCommitSession) AutoStaged() bool { return f.autoStaged }
func (f *fakeACPCommitSession) Regenerate(context.Context) error {
	f.regenerated++
	f.proposal = min(f.proposal+1, len(f.proposals)-1)
	return nil
}
func (f *fakeACPCommitSession) Commit(context.Context) (commitagent.Result, error) {
	f.committed++
	return commitagent.Result{Messages: []string{"feat(acp): add commit workflow"}}, nil
}
func (f *fakeACPCommitSession) Push(context.Context) error { f.pushed++; return f.pushErr }
func (f *fakeACPCommitSession) Close() error               { f.closed++; return nil }

func TestACPAgentRegeneratesThenCommitsAndPushes(t *testing.T) {
	agent, workspaceStore, connection := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	commitSession := &fakeACPCommitSession{
		autoStaged: true,
		proposals: [][]commitagent.Proposal{
			{{Type: "chore", Summary: "first proposal", Files: []string{"old.go"}}},
			{{Type: "feat", Scope: "acp", Summary: "add commit workflow", Files: []string{"app/acp.go", "app/acp_commit.go"}}},
		},
	}
	agent.commitSession = func(context.Context, commitagent.ProgressFunc) (acpCommitSession, error) {
		return commitSession, nil
	}
	connection.elicitationContents = []map[string]any{
		{"action": "regenerate"},
		{"action": "commit_push"},
	}
	if _, err := agent.Initialize(t.Context(), acp.InitializeRequest{ClientCapabilities: acp.ClientCapabilities{
		Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
	}}); err != nil {
		t.Fatal(err)
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/commit")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || commitSession.regenerated != 1 ||
		commitSession.committed != 1 || commitSession.pushed != 1 || commitSession.closed != 1 {
		t.Fatalf("response = %#v, session = %#v", response, commitSession)
	}
	elicitations := connection.elicitationSnapshot()
	if len(elicitations) != 2 || elicitations[1].Form == nil ||
		!strings.Contains(elicitations[1].Form.Message, "feat(acp): add commit workflow") ||
		!strings.Contains(elicitations[1].Form.Message, "app/acp_commit.go") ||
		!strings.Contains(elicitations[1].Form.Message, "leaves them staged") {
		t.Fatalf("commit elicitations = %#v", elicitations)
	}
	action, ok := elicitations[1].Form.RequestedSchema.Properties["action"].(map[string]any)
	if !ok || action["type"] != "string" || action["oneOf"] == nil {
		t.Fatalf("commit action schema = %#v", elicitations[1].Form.RequestedSchema.Properties)
	}
	if err := elicitations[1].Validate(); err != nil {
		t.Fatalf("commit elicitation schema is invalid: %v", err)
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if !strings.Contains(output, "Commit created") || !strings.Contains(output, "Push completed") {
		t.Fatalf("commit output = %q", output)
	}
}

func TestACPAgentCommitRequiresFormElicitation(t *testing.T) {
	agent, workspaceStore, connection := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	called := false
	agent.commitSession = func(context.Context, commitagent.ProgressFunc) (acpCommitSession, error) {
		called = true
		return nil, errors.New("must not prepare")
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/commit")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if response.StopReason != acp.StopReasonEndTurn || called || !strings.Contains(output, "form elicitation") {
		t.Fatalf("response = %#v, called = %v, output = %q", response, called, output)
	}
}

func TestACPAgentRunsExplicitSearchCommand(t *testing.T) {
	parentClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, parentClient, &fakeAgentTools{})
	var received subagent.ExternalSearchInput
	agent.state.toolRuntime = testAgentSearchRuntime(t, func(_ context.Context, input subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		received = input
		return subagent.ExternalSearchResult{
			Agent: "codex", Summary: "ACP evidence: https://agentclientprotocol.com",
		}, nil
	})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/agent:search current ACP lifecycle")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || received.Query != "current ACP lifecycle" ||
		len(received.CompletionCriteria) == 0 {
		t.Fatalf("response = %#v, input = %#v", response, received)
	}
	var output, thought string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
		if update := notification.Update.AgentThoughtChunk; update != nil && update.Content.Text != nil {
			thought += update.Content.Text.Text
		}
	}
	if output != "reply 1" || !strings.Contains(thought, "Search agent started") ||
		!strings.Contains(thought, "Main agent is preparing") {
		t.Fatalf("output = %q, thought = %q", output, thought)
	}
	if len(parentClient.requests) != 1 ||
		!strings.Contains(parentClient.requests[0].Messages[len(parentClient.requests[0].Messages)-1].Content, "https://agentclientprotocol.com") ||
		!strings.Contains(parentClient.requests[0].Messages[len(parentClient.requests[0].Messages)-1].Content, "loom_ref") {
		t.Fatalf("parent requests = %#v", parentClient.requests)
	}
}

func TestACPAgentRunsApprovedPlanThroughElicitation(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{
			"objective":"Expose plan through ACP",
			"conditions":["Execute only after approval"],
			"acceptance_criteria":["ACP receives the completed result"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitPlanToolName, `{
			"outcome":"succeeded",
			"summary":"Run the shared plan workflow through ACP",
			"conditions":["Execute only after approval"],
			"steps":[{"title":"Connect ACP","description":"Use the shared plan workflow","target":{"any":[{"all":[{"kind":"paths","paths":["app/acp.go"]}]}]}}],
			"verification":["Complete the approved plan cycle"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.CoderCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Connected ACP to the shared plan workflow",
			"artifacts":["app/acp.go"],
			"verification":["go test ./app"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ReviewTaskToolName, `{
			"decision":"next",
			"feedback":"",
			"facts":["ACP plan execution completed"]
		}`)}},
	}}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	connection.answer = "approve"
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
		Prompt:    []acp.ContentBlock{acp.TextBlock("/plan expose plan through ACP")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 4 {
		t.Fatalf("response = %#v, requests = %d", response, len(configuredClient.requests))
	}
	elicitations := connection.elicitationSnapshot()
	if len(elicitations) != 1 || elicitations[0].Form == nil || elicitations[0].Form.SessionId != sessionID {
		t.Fatalf("elicitation scope = %#v, want session %q", elicitations, sessionID)
	}
	wireRequest, err := json.Marshal(elicitations[0])
	if err != nil || !strings.Contains(string(wireRequest), `"sessionId":"`+string(sessionID)+`"`) {
		t.Fatalf("elicitation wire request = %s, err = %v", wireRequest, err)
	}

	var output, thought string
	var statuses []acp.PlanEntryStatus
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
		if update := notification.Update.AgentThoughtChunk; update != nil && update.Content.Text != nil {
			thought += update.Content.Text.Text
		}
		if update := notification.Update.Plan; update != nil && len(update.Entries) == 1 && update.Entries[0].Content == "expose plan through ACP" {
			statuses = append(statuses, update.Entries[0].Status)
		}
	}
	if !strings.Contains(output, "Plan executed successfully.") || !strings.Contains(output, "Connected ACP to the shared plan workflow") {
		t.Fatalf("plan output = %q", output)
	}
	if !strings.Contains(thought, "griller") || !strings.Contains(thought, "planner") || !strings.Contains(thought, "executor") {
		t.Fatalf("plan progress = %q", thought)
	}
	if len(statuses) != 2 || statuses[0] != acp.PlanEntryStatusInProgress || statuses[1] != acp.PlanEntryStatusCompleted {
		t.Fatalf("plan statuses = %#v", statuses)
	}
	saved, err := activeACPWorkspaceStore(t, agent).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Transcript) != 2 || saved.Transcript[0].Content != "expose plan through ACP" ||
		!strings.Contains(saved.Transcript[1].Content, "Plan executed successfully.") {
		t.Fatalf("saved transcript = %#v", saved.Transcript)
	}
}

func TestACPAgentStopsPlanWhenElicitationFails(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{planToolCall(subagent.AskToUserToolName, `{
			"question":"Which persistence should be used?",
			"choices":[{"id":"sqlite","label":"SQLite"}]
		}`)},
	}}}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	connection.elicitationErr = errors.New("unknown elicitation scope")
	if _, err := agent.Initialize(t.Context(), acp.InitializeRequest{
		ClientCapabilities: acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	_, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/plan add persistence")},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown elicitation scope") {
		t.Fatalf("plan elicitation error = %v", err)
	}
	if len(configuredClient.requests) != 1 {
		t.Fatalf("plan continued after elicitation failure: %d requests", len(configuredClient.requests))
	}
	elicitations := connection.elicitationSnapshot()
	if len(elicitations) != 1 || elicitations[0].Form == nil || elicitations[0].Form.SessionId != sessionID {
		t.Fatalf("failed elicitation scope = %#v", elicitations)
	}
}

func TestACPAgentCancelsPlanWhenElicitationIsCancelled(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{planToolCall(subagent.AskToUserToolName, `{
			"question":"Which persistence should be used?"
		}`)},
	}}}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	connection.elicitationAction = "cancel"
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
		Prompt:    []acp.ContentBlock{acp.TextBlock("/plan add persistence")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonCancelled || len(configuredClient.requests) != 1 {
		t.Fatalf("cancelled plan response = %#v, requests = %d", response, len(configuredClient.requests))
	}
}

func TestACPAgentExplainsPlanElicitationRequirement(t *testing.T) {
	configuredClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/plan make a change")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 0 {
		t.Fatalf("response = %#v, requests = %d", response, len(configuredClient.requests))
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if !strings.Contains(output, "form elicitation") {
		t.Fatalf("unsupported client output = %q", output)
	}
	if saved, err := activeACPWorkspaceStore(t, agent).Load(); err != nil || len(saved.Transcript) != 0 {
		t.Fatalf("unsupported plan changed the empty session: saved=%#v err=%v", saved, err)
	}
}

func TestACPAgentClearKeepsSessionAndDropsConversationProjection(t *testing.T) {
	configuredClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("before clear")},
	}); err != nil {
		t.Fatal(err)
	}
	runID := agent.state.runID
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/clear")},
	}); err != nil {
		t.Fatal(err)
	}
	if agent.sessionID != sessionID || agent.state.runID != runID || !agent.sessionOpen {
		t.Fatalf("clear changed active session: id=%q run=%q open=%v", agent.sessionID, agent.state.runID, agent.sessionOpen)
	}
	if cleared, err := activeACPWorkspaceStore(t, agent).Load(); err != nil || len(cleared.Transcript) != 0 {
		t.Fatalf("workspace projection after clear: saved=%#v err=%v", cleared, err)
	}
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("after clear")},
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := activeACPWorkspaceStore(t, agent).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Transcript) != 2 || saved.Transcript[0].Content != "after clear" || saved.Transcript[1].Content != "reply 2" {
		t.Fatalf("transcript after clear = %#v", saved.Transcript)
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if !strings.Contains(output, "Conversation cleared for this workspace.") {
		t.Fatalf("clear output = %q", output)
	}
}

func TestACPAgentClearReportsPlanCheckpointRemovalFailure(t *testing.T) {
	agent, workspaceStore, connection := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	activeStore := activeACPWorkspaceStore(t, agent)
	if err := os.MkdirAll(activeStore.ExecutionPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeStore.ExecutionPath(), "blocked"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	updatesBefore := len(connection.snapshot())
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/clear")},
	}); err == nil {
		t.Fatal("/clear hid the plan checkpoint removal failure")
	}
	for _, notification := range connection.snapshot()[updatesBefore:] {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil &&
			strings.Contains(update.Content.Text.Text, "Conversation cleared") {
			t.Fatalf("/clear emitted a success message after failure: %#v", notification)
		}
	}
}

func TestACPAgentRestoresCapturedWorkspaceMCPOnClose(t *testing.T) {
	tools := &fakeACPExternalTools{}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	base := mcpconfig.Default()
	base.Servers["workspace"] = mcpconfig.ServerConfig{
		Transport: mcpconfig.TransportStdio, Command: "workspace-mcp",
	}
	base.Roles[mcpconfig.RoleDefault] = []string{"workspace"}
	if err := agent.state.mcpSettingsStore.Save(base); err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd: workspaceStore.Root,
		McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{
			Name: "client", Command: "client-mcp",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.configured) != 1 || len(tools.configured[0].Servers) != 2 {
		t.Fatalf("session MCP config = %#v", tools.configured)
	}
	if err := os.WriteFile(agent.state.mcpSettingsStore.Path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := agent.CloseSession(cancelled, acp.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
	if len(tools.configured) != 2 {
		t.Fatalf("MCP configure calls = %d", len(tools.configured))
	}
	restored := tools.configured[1]
	if len(restored.Servers) != 1 || restored.Servers["workspace"].Command != "workspace-mcp" {
		t.Fatalf("restored MCP config = %#v", restored)
	}
}

func TestACPAgentRestoresWorkspaceMCPWhenActivationContextIsCancelled(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	tools := &fakeACPExternalTools{cancelFirst: cancel}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	base := mcpconfig.Default()
	base.Servers["workspace"] = mcpconfig.ServerConfig{
		Transport: mcpconfig.TransportStdio, Command: "workspace-mcp",
	}
	if err := agent.state.mcpSettingsStore.Save(base); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewSession(requestContext, acp.NewSessionRequest{
		Cwd: workspaceStore.Root,
		McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{
			Name: "client", Command: "client-mcp",
		}}},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("session/new error = %v, want context cancellation", err)
	}
	if len(tools.configured) != 2 || len(tools.configured[0].Servers) != 2 || len(tools.configured[1].Servers) != 1 {
		t.Fatalf("MCP configs after cancelled activation = %#v", tools.configured)
	}
	if _, exists := tools.configured[1].Servers["workspace"]; !exists {
		t.Fatalf("baseline MCP was not restored: %#v", tools.configured[1])
	}
}

func TestACPAgentCancelsLearningBeforeSlowMCPCleanup(t *testing.T) {
	tools := &fakeACPExternalTools{
		blockCall: 2, blockEntered: make(chan struct{}), blockRelease: make(chan struct{}),
	}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	learningContext := agent.state.learningCtx
	closed := make(chan error, 1)
	go func() {
		_, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: sessionID})
		closed <- err
	}()
	select {
	case <-tools.blockEntered:
	case <-time.After(time.Second):
		t.Fatal("MCP cleanup did not start")
	}
	select {
	case <-learningContext.Done():
	default:
		t.Fatal("session learning was still running when MCP cleanup began")
	}
	close(tools.blockRelease)
	if err := <-closed; err != nil {
		t.Fatal(err)
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

	saved, err := activeACPWorkspaceStore(t, agent).Load()
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
