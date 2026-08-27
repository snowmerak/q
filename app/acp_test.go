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

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/commitagent"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
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
	blockUpdates        bool
	updateEntered       chan struct{}
	updateOnce          sync.Once
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

type routingBlockingACPClient struct {
	mu      sync.Mutex
	started map[string]chan struct{}
}

func (b *routingBlockingACPClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	key := ""
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if request.Messages[index].Role == client.RoleUser {
			key = request.Messages[index].TextContent()
			break
		}
	}
	b.mu.Lock()
	started := b.started[key]
	if started != nil {
		close(started)
		delete(b.started, key)
	}
	b.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *routingBlockingACPClient) ListModels(context.Context) ([]client.Model, error) {
	return nil, nil
}
func (b *routingBlockingACPClient) Close() error { return nil }

func (b *blockingACPClient) Chat(ctx context.Context, _ client.ChatRequest) (*client.ChatResponse, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingACPClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (b *blockingACPClient) Close() error                                       { return nil }

func (f *fakeACPConnection) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	f.mu.Lock()
	f.updates = append(f.updates, notification)
	block := f.blockUpdates
	entered := f.updateEntered
	f.mu.Unlock()
	if block {
		if entered != nil {
			f.updateOnce.Do(func() { close(entered) })
		}
		<-ctx.Done()
		return ctx.Err()
	}
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

func activeACPRuntime(t *testing.T, agent *acpAgent, sessionIDs ...acp.SessionId) *acpAgent {
	t.Helper()
	if agent == nil {
		t.Fatal("ACP agent is unavailable")
	}
	agent.stateMu.Lock()
	defer agent.stateMu.Unlock()
	if len(sessionIDs) == 1 {
		if runtime := agent.sessions[sessionIDs[0]]; runtime != nil {
			return runtime
		}
		t.Fatalf("ACP session %q is not active", sessionIDs[0])
	}
	if len(agent.sessions) != 1 {
		t.Fatalf("ACP agent has %d active sessions; specify one", len(agent.sessions))
	}
	for _, runtime := range agent.sessions {
		return runtime
	}
	t.Fatal("ACP agent has no active session")
	return nil
}

func activeACPWorkspaceStore(t *testing.T, agent *acpAgent, sessionIDs ...acp.SessionId) workspace.Store {
	t.Helper()
	runtime := activeACPRuntime(t, agent, sessionIDs...)
	if runtime.state == nil || runtime.state.workspaceStore == nil || runtime.state.workspaceStore.SessionID == "" {
		t.Fatal("ACP session has no active workspace store")
	}
	return *runtime.state.workspaceStore
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

func TestACPAgentKeepsMultipleActiveSessionsPerProcess(t *testing.T) {
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

	first := openTestACPSession(t, agent, workspaceStore.Root)
	secondResponse, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd: workspaceStore.Root, McpServers: []acp.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	second := secondResponse.SessionId
	if second == first {
		t.Fatalf("second session reused ID %q", first)
	}
	if _, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd: t.TempDir(), McpServers: []acp.McpServer{},
	}); err == nil {
		t.Fatal("session for a different workspace was accepted")
	}
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: first, Prompt: []acp.ContentBlock{acp.TextBlock("first session")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: second, Prompt: []acp.ContentBlock{acp.TextBlock("second session")},
	}); err != nil {
		t.Fatal(err)
	}
	firstSaved, err := activeACPWorkspaceStore(t, agent, first).Load()
	if err != nil || len(firstSaved.Transcript) != 2 || firstSaved.Transcript[0].Content != "first session" {
		t.Fatalf("first session projection = %#v, %v", firstSaved.Transcript, err)
	}
	secondSaved, err := activeACPWorkspaceStore(t, agent, second).Load()
	if err != nil || len(secondSaved.Transcript) != 2 || secondSaved.Transcript[0].Content != "second session" {
		t.Fatalf("second session projection = %#v, %v", secondSaved.Transcript, err)
	}

	if _, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: first}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: second, Prompt: []acp.ContentBlock{acp.TextBlock("second remains active")},
	}); err != nil {
		t.Fatalf("closing first session disrupted second: %v", err)
	}
	listed, err := agent.ListSessions(t.Context(), acp.ListSessionsRequest{Cwd: &workspaceStore.Root})
	if err != nil {
		t.Fatal(err)
	}
	listedIDs := make([]acp.SessionId, 0, len(listed.Sessions))
	for _, session := range listed.Sessions {
		listedIDs = append(listedIDs, session.SessionId)
	}
	if len(listedIDs) != 2 || !slices.Contains(listedIDs, first) || !slices.Contains(listedIDs, second) {
		t.Fatalf("listed session IDs = %#v", listedIDs)
	}
	if _, err := agent.UnstableDeleteSession(t.Context(), acp.UnstableDeleteSessionRequest{SessionId: first}); err != nil {
		t.Fatal(err)
	}
	listed, err = agent.ListSessions(t.Context(), acp.ListSessionsRequest{})
	if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].SessionId != second {
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

func TestACPAgentShutdownWaitsForUnpublishedSessionActivation(t *testing.T) {
	tools := &fakeACPExternalTools{
		blockCall: 1, blockEntered: make(chan struct{}), blockRelease: make(chan struct{}),
	}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	created := make(chan error, 1)
	go func() {
		_, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
			Cwd:        workspaceStore.Root,
			McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "slow", Command: "slow-mcp"}}},
		})
		created <- err
	}()
	select {
	case <-tools.blockEntered:
	case <-time.After(time.Second):
		t.Fatal("session activation did not reach the MCP barrier")
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- agent.shutdown() }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before activation released: %v", err)
	default:
	}
	close(tools.blockRelease)
	if err := <-created; err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("activation result after shutdown = %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	agent.stateMu.Lock()
	active := len(agent.sessions)
	agent.stateMu.Unlock()
	if active != 0 {
		t.Fatalf("shutdown published %d late session runtimes", active)
	}
}

func TestACPAgentShutdownCancelsTurnsBeforeWaitingForActivation(t *testing.T) {
	tools := &fakeACPExternalTools{}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	activeID := openTestACPSession(t, agent, workspaceStore.Root)
	servers := []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "session", Command: "session-mcp"}}}
	created, err := agent.NewSession(t.Context(), acp.NewSessionRequest{Cwd: workspaceStore.Root, McpServers: servers})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}

	agent.mcpMu.Lock()
	locked := true
	defer func() {
		if locked {
			agent.mcpMu.Unlock()
		}
	}()
	promptDone := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(t.Context(), acp.PromptRequest{
			SessionId: activeID, Prompt: []acp.ContentBlock{acp.TextBlock("/help")},
		})
		promptDone <- err
	}()
	activeRuntime := activeACPRuntime(t, agent, activeID)
	waitUntil := func(message string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal(message)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitUntil("prompt did not install its turn cancellation", func() bool {
		activeRuntime.stateMu.Lock()
		defer activeRuntime.stateMu.Unlock()
		return activeRuntime.turnCancel != nil
	})
	resumeDone := make(chan error, 1)
	go func() {
		_, err := agent.ResumeSession(t.Context(), acp.ResumeSessionRequest{
			SessionId: created.SessionId, Cwd: workspaceStore.Root, McpServers: servers,
		})
		resumeDone <- err
	}()
	waitUntil("resume did not reserve its activation", func() bool {
		agent.stateMu.Lock()
		defer agent.stateMu.Unlock()
		return agent.opening[created.SessionId] != nil
	})
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- agent.shutdown() }()
	waitUntil("shutdown waited for activation before marking the active turn closing", func() bool {
		activeRuntime.stateMu.Lock()
		defer activeRuntime.stateMu.Unlock()
		return activeRuntime.closing
	})
	agent.mcpMu.Unlock()
	locked = false
	if err := <-promptDone; err == nil && !activeRuntime.closing {
		t.Fatalf("prompt unexpectedly continued through shutdown: %v", err)
	}
	if err := <-resumeDone; err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("resume result after shutdown = %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}

func TestACPAgentActiveLoadAndResumeAreIdempotentForSameMCPServers(t *testing.T) {
	tools := &fakeACPExternalTools{}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	servers := []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "client", Command: "client-mcp"}}}
	created, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd: workspaceStore.Root, McpServers: servers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ResumeSession(t.Context(), acp.ResumeSessionRequest{
		SessionId: created.SessionId, Cwd: workspaceStore.Root, McpServers: servers,
	}); err != nil {
		t.Fatalf("idempotent resume: %v", err)
	}
	if _, err := agent.LoadSession(t.Context(), acp.LoadSessionRequest{
		SessionId: created.SessionId, Cwd: workspaceStore.Root, McpServers: servers,
	}); err != nil {
		t.Fatalf("idempotent load: %v", err)
	}
	if len(tools.configured) != 2 {
		t.Fatalf("idempotent lifecycle reconfigured MCP %d times", len(tools.configured))
	}
	different := []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "other", Command: "other-mcp"}}}
	if _, err := agent.ResumeSession(t.Context(), acp.ResumeSessionRequest{
		SessionId: created.SessionId, Cwd: workspaceStore.Root, McpServers: different,
	}); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("different active MCP configuration error = %v", err)
	}
}

func TestACPAgentConcurrentResumeSharesOneSessionRuntime(t *testing.T) {
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	if _, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := agent.ResumeSession(t.Context(), acp.ResumeSessionRequest{
				SessionId: sessionID, Cwd: workspaceStore.Root,
			})
			errors <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	agent.stateMu.Lock()
	active := len(agent.sessions)
	agent.stateMu.Unlock()
	if active != 1 {
		t.Fatalf("concurrent resume created %d runtimes", active)
	}
}

func TestACPAgentCloseWaitsForSessionActivation(t *testing.T) {
	tools := &fakeACPExternalTools{
		blockCall: 3, blockEntered: make(chan struct{}), blockRelease: make(chan struct{}),
	}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	servers := []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "session", Command: "session-mcp"}}}
	created, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
		Cwd: workspaceStore.Root, McpServers: servers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: created.SessionId}); err != nil {
		t.Fatal(err)
	}
	resumeDone := make(chan error, 1)
	go func() {
		_, err := agent.ResumeSession(t.Context(), acp.ResumeSessionRequest{
			SessionId: created.SessionId, Cwd: workspaceStore.Root, McpServers: servers,
		})
		resumeDone <- err
	}()
	select {
	case <-tools.blockEntered:
	case <-time.After(time.Second):
		t.Fatal("resume did not reach its activation barrier")
	}
	closeDone := make(chan error, 1)
	go func() {
		_, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: created.SessionId})
		closeDone <- err
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("close returned before activation completed: %v", err)
	default:
	}
	close(tools.blockRelease)
	if err := <-resumeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	agent.stateMu.Lock()
	_, active := agent.sessions[created.SessionId]
	agent.stateMu.Unlock()
	if active {
		t.Fatal("session became active after close returned")
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
	activeStore := activeACPWorkspaceStore(t, agent)
	savedSession, err := activeStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := resumablePlanCheckpoint(subagent.ExecutionPhaseTarget)
	checkpoint.RunID = savedSession.RunID
	checkpoint.Plan.Steps = append(checkpoint.Plan.Steps, subagent.PlanStep{
		Title: "Verify restored TODOs", Description: "Replay every saved step",
		Target: subagent.TargetCondition{Any: []subagent.TargetProduct{{All: []subagent.TargetSelector{{
			Kind: subagent.TargetSelectorPaths, Paths: []string{"app/acp_test.go"},
		}}}}},
	})
	if err := activeStore.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	closedRuntime := activeACPRuntime(t, agent, sessionID)
	if _, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	if closedRuntime.state.memory != nil || closedRuntime.state.learning != nil || len(closedRuntime.state.messages) != 0 ||
		closedRuntime.state.activeTask != nil || closedRuntime.state.workspaceStore == nil || closedRuntime.state.workspaceStore.SessionID != "" {
		t.Fatalf("closed ACP session retained in-memory state: %#v", closedRuntime.state.workspaceStore)
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
	var restoredPlan bool
	for _, notification := range restartedConnection.snapshot() {
		if chunk := notification.Update.UserMessageChunk; chunk != nil && chunk.Content.Text != nil {
			userText += chunk.Content.Text.Text
		}
		if chunk := notification.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			assistantText += chunk.Content.Text.Text
		}
		if update := notification.Update.Plan; update != nil && len(update.Entries) == 2 &&
			update.Entries[0].Content == "Connect persistence" &&
			update.Entries[0].Status == acp.PlanEntryStatusInProgress &&
			update.Entries[1].Content == "Verify restored TODOs" &&
			update.Entries[1].Status == acp.PlanEntryStatusPending {
			restoredPlan = true
		}
	}
	if userText != "persist me" || assistantText != "reply 1" || !restoredPlan {
		t.Fatalf("loaded session: user=%q assistant=%q restored_plan=%v", userText, assistantText, restoredPlan)
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
	configuredClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.ReasoningEffort = "high"
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
	if len(configuredClient.requests) != 1 || configuredClient.requests[0].ReasoningEffort != "high" {
		t.Fatalf("ACP requests = %#v", configuredClient.requests)
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
			"steps":[
				{"title":"Connect ACP","description":"Use the shared plan workflow","target":{"any":[{"all":[{"kind":"paths","paths":["app/acp.go"]}]}]}},
				{"title":"Verify TODOs","description":"Expose every plan step","target":{"any":[{"all":[{"kind":"paths","paths":["app/acp_test.go"]}]}]}}
			],
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
			"facts":["ACP plan execution advanced"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.CoderCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Exposed every plan step as an ACP TODO",
			"artifacts":["app/acp_test.go"],
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
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 6 {
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
	var objectiveStatuses []acp.PlanEntryStatus
	var sawInitialSteps, sawAdvancedSteps, sawCompletedSteps bool
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
		if update := notification.Update.AgentThoughtChunk; update != nil && update.Content.Text != nil {
			thought += update.Content.Text.Text
		}
		if update := notification.Update.Plan; update != nil && len(update.Entries) == 1 && update.Entries[0].Content == "expose plan through ACP" {
			objectiveStatuses = append(objectiveStatuses, update.Entries[0].Status)
		}
		if update := notification.Update.Plan; update != nil && len(update.Entries) == 2 &&
			update.Entries[0].Content == "Connect ACP" && update.Entries[1].Content == "Verify TODOs" {
			switch {
			case update.Entries[0].Status == acp.PlanEntryStatusInProgress && update.Entries[1].Status == acp.PlanEntryStatusPending:
				sawInitialSteps = true
			case update.Entries[0].Status == acp.PlanEntryStatusCompleted && update.Entries[1].Status == acp.PlanEntryStatusInProgress:
				sawAdvancedSteps = true
			case update.Entries[0].Status == acp.PlanEntryStatusCompleted && update.Entries[1].Status == acp.PlanEntryStatusCompleted:
				sawCompletedSteps = true
			}
		}
	}
	if !strings.Contains(output, "Plan executed successfully.") || !strings.Contains(output, "Connected ACP to the shared plan workflow") {
		t.Fatalf("plan output = %q", output)
	}
	if !strings.Contains(thought, "griller") || !strings.Contains(thought, "planner") || !strings.Contains(thought, "executor") {
		t.Fatalf("plan progress = %q", thought)
	}
	if len(objectiveStatuses) != 1 || objectiveStatuses[0] != acp.PlanEntryStatusInProgress ||
		!sawInitialSteps || !sawAdvancedSteps || !sawCompletedSteps {
		t.Fatalf(
			"plan lifecycle: objective=%#v initial=%v advanced=%v completed=%v",
			objectiveStatuses, sawInitialSteps, sawAdvancedSteps, sawCompletedSteps,
		)
	}
	saved, err := activeACPWorkspaceStore(t, agent).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Transcript) != 2 || saved.Transcript[0].Content != "expose plan through ACP" ||
		!strings.Contains(saved.Transcript[1].Content, "Plan executed successfully.") {
		t.Fatalf("saved transcript = %#v", saved.Transcript)
	}
	assertACPTraceLifecycles(t, connection.snapshot(), 6, 0)
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
	assertACPTraceLifecycles(t, connection.snapshot(), 1, 1)
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
	assertACPTraceLifecycles(t, connection.snapshot(), 1, 1)
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
	runtime := activeACPRuntime(t, agent, sessionID)
	runID := runtime.state.runID
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/clear")},
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.sessionID != sessionID || runtime.state.runID != runID || !runtime.sessionOpen {
		t.Fatalf("clear changed active session: id=%q run=%q open=%v", runtime.sessionID, runtime.state.runID, runtime.sessionOpen)
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

func TestACPAgentRestoresCapturedWorkspaceMCPAfterFallbackValidation(t *testing.T) {
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
	if len(tools.configured) != 2 || len(tools.configured[0].Servers) != 2 {
		t.Fatalf("session MCP config = %#v", tools.configured)
	}
	restored := tools.configured[1]
	if len(restored.Servers) != 1 || restored.Servers["workspace"].Command != "workspace-mcp" {
		t.Fatalf("restored MCP config = %#v", restored)
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
		t.Fatalf("closing an idle session unexpectedly reconfigured MCP: %d calls", len(tools.configured))
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

func TestACPAgentFallbackMCPConfigurationFollowsPromptSession(t *testing.T) {
	tools := &fakeACPExternalTools{}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	create := func(command string) acp.SessionId {
		t.Helper()
		response, err := agent.NewSession(t.Context(), acp.NewSessionRequest{
			Cwd:        workspaceStore.Root,
			McpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: command, Command: command}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return response.SessionId
	}
	first := create("first-mcp")
	second := create("second-mcp")
	for _, sessionID := range []acp.SessionId{first, second} {
		if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/help")},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(tools.configured) != 8 {
		t.Fatalf("MCP configure calls = %d, want activation and prompt switch/restore pairs", len(tools.configured))
	}
	commandAt := func(index int) string {
		for id, server := range tools.configured[index].Servers {
			if strings.HasPrefix(id, "acp_session_") {
				return server.Command
			}
		}
		return ""
	}
	if commandAt(4) != "first-mcp" || commandAt(6) != "second-mcp" ||
		len(tools.configured[5].Servers) != 0 || len(tools.configured[7].Servers) != 0 {
		t.Fatalf("per-session MCP switches = %#v", tools.configured[4:])
	}
}

func TestACPAgentCancelsSessionLearningOnClose(t *testing.T) {
	tools := &fakeACPExternalTools{}
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, tools)
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	learningContext := activeACPRuntime(t, agent, sessionID).state.learningCtx
	if _, err := agent.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-learningContext.Done():
	default:
		t.Fatal("session learning was still running after close")
	}
}

func TestACPAgentLearningControlUpdatesEveryActiveSession(t *testing.T) {
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	first := openTestACPSession(t, agent, workspaceStore.Root)
	second := openTestACPSession(t, agent, workspaceStore.Root)
	firstRuntime := activeACPRuntime(t, agent, first)
	secondRuntime := activeACPRuntime(t, agent, second)
	firstContext := firstRuntime.state.learningCtx
	secondContext := secondRuntime.state.learningCtx
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: first, Prompt: []acp.ContentBlock{acp.TextBlock("/learn off")},
	}); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []*acpAgent{firstRuntime, secondRuntime} {
		if !runtime.state.learningDisabled() || runtime.state.learningCtx != nil {
			t.Fatalf("learning was not disabled for session %q", runtime.sessionID)
		}
	}
	for _, learningContext := range []context.Context{firstContext, secondContext} {
		select {
		case <-learningContext.Done():
		default:
			t.Fatal("an active session's Thinker context was not cancelled")
		}
	}
	if _, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: second, Prompt: []acp.ContentBlock{acp.TextBlock("/learn on")},
	}); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []*acpAgent{firstRuntime, secondRuntime} {
		if runtime.state.learningDisabled() || runtime.state.learningCtx == nil {
			t.Fatalf("learning was not enabled for session %q", runtime.sessionID)
		}
		select {
		case <-runtime.state.learningCtx.Done():
			t.Fatalf("session %q received an already-cancelled Thinker context", runtime.sessionID)
		default:
		}
	}
}

func TestACPAgentShutdownCancelsBlockedLearningStatusUpdate(t *testing.T) {
	agent, workspaceStore, connection := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	connection.mu.Lock()
	connection.blockUpdates = true
	connection.updateEntered = make(chan struct{})
	entered := connection.updateEntered
	connection.mu.Unlock()
	promptDone := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(context.Background(), acp.PromptRequest{
			SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/learn status")},
		})
		promptDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("learning status did not reach the blocked session update")
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- agent.shutdown() }()
	select {
	case err := <-promptDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked learning status error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel the blocked learning status turn")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown remained blocked behind learning status")
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

func TestACPAgentCancelIsScopedToOneOfMultipleSessions(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	configuredClient := &routingBlockingACPClient{started: map[string]chan struct{}{
		"first turn": firstStarted, "second turn": secondStarted,
	}}
	agent, workspaceStore, _ := testACPAgent(t, configuredClient, &fakeAgentTools{})
	first := openTestACPSession(t, agent, workspaceStore.Root)
	second := openTestACPSession(t, agent, workspaceStore.Root)
	type promptResult struct {
		response acp.PromptResponse
		err      error
	}
	run := func(sessionID acp.SessionId, text string) <-chan promptResult {
		result := make(chan promptResult, 1)
		go func() {
			response, err := agent.Prompt(t.Context(), acp.PromptRequest{
				SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock(text)},
			})
			result <- promptResult{response: response, err: err}
		}()
		return result
	}
	firstResult := run(first, "first turn")
	secondResult := run(second, "second turn")
	for _, started := range []chan struct{}{firstStarted, secondStarted} {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("one of the independent session turns did not start")
		}
	}
	if err := agent.Cancel(t.Context(), acp.CancelNotification{SessionId: first}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-firstResult:
		if result.err != nil || result.response.StopReason != acp.StopReasonCancelled {
			t.Fatalf("first cancellation = %#v, %v", result.response, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first session did not cancel")
	}
	select {
	case result := <-secondResult:
		t.Fatalf("cancelling first session stopped second: %#v, %v", result.response, result.err)
	default:
	}
	if err := agent.Cancel(t.Context(), acp.CancelNotification{SessionId: second}); err != nil {
		t.Fatal(err)
	}
	result := <-secondResult
	if result.err != nil || result.response.StopReason != acp.StopReasonCancelled {
		t.Fatalf("second cancellation = %#v, %v", result.response, result.err)
	}
}

func TestACPAgentCloseStopsPromptBeforeTurnCancelIsInstalled(t *testing.T) {
	configuredClient := &blockingACPClient{started: make(chan struct{})}
	agent, workspaceStore, _ := testACPAgent(t, configuredClient, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	locked := make(chan struct{})
	release := make(chan struct{})
	runtime.promptLocked = func() {
		close(locked)
		<-release
	}
	promptDone := make(chan error, 1)
	go func() {
		_, err := agent.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("must not start")},
		})
		promptDone <- err
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("prompt did not reach the pre-turn barrier")
	}
	closeDone := make(chan error, 1)
	go func() {
		_, err := agent.CloseSession(t.Context(), acp.CloseSessionRequest{SessionId: sessionID})
		closeDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		runtime.stateMu.Lock()
		closing := runtime.closing
		runtime.stateMu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close did not mark the runtime before waiting for its prompt")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-promptDone; err == nil {
		t.Fatal("prompt started after its session began closing")
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-configuredClient.started:
		t.Fatal("model turn started after close began")
	default:
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
