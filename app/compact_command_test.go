package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

func compactCommandHistory() []client.Message {
	return []client.Message{
		{Role: client.RoleUser, Content: strings.Repeat("old context ", 180)},
		{Role: client.RoleAssistant, Content: strings.Repeat("old answer ", 120)},
		{Role: client.RoleUser, Content: "latest request"},
	}
}

func TestTUICompactCommandPreservesTranscriptWithoutSendingChat(t *testing.T) {
	store := workspace.Store{Root: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 4000
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &store
	m.enterChat(value, fake)
	m.input.SetValue("/comp")
	if matches := m.slashCompletionMatches(); len(matches) != 1 || matches[0].name != "/compact" {
		t.Fatalf("/compact completion = %#v", matches)
	}
	history := compactCommandHistory()
	for _, message := range history {
		m.messages = append(m.messages, message)
		m.memory.Append(message)
	}
	if m.memory.ShouldCompact() {
		t.Fatal("test history must be below the automatic compaction trigger")
	}
	before := len(m.messages)
	m.input.SetValue("/compact")
	updated, command := m.submitChat()
	m = updated.(model)
	if !m.compacting || !m.waiting || command == nil || m.input.Value() != "" {
		t.Fatalf("command did not start compaction: waiting=%v compacting=%v input=%q", m.waiting, m.compacting, m.input.Value())
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatal("manual compaction did not return a batch")
	}
	var result compactionResultMsg
	found := false
	for _, child := range batch {
		if message, ok := child().(compactionResultMsg); ok {
			result, found = message, true
		}
	}
	if !found || !result.manual || len(fake.requests) != 1 || !isCheckpointRequestForTest(fake.requests[0]) {
		t.Fatalf("checkpoint request = %#v, result = %#v", fake.requests, result)
	}
	updated, _ = m.Update(result)
	m = updated.(model)
	if m.waiting || m.compacting || m.turnCancel != nil || m.compactionTarget != 0 || len(fake.requests) != 1 || len(m.messages) != before {
		t.Fatalf("manual compaction continued chat or changed transcript: waiting=%v requests=%d transcript=%d", m.waiting, len(fake.requests), len(m.messages))
	}
	if !hasMessageNamed(m.memory.Messages(), memory.SummaryName) || m.conversationID != "" {
		t.Fatalf("context was not compacted: %#v, conversation=%q", m.memory.Messages(), m.conversationID)
	}
	saved, err := store.Load()
	if err != nil || !hasMessageNamed(saved.Context, memory.SummaryName) {
		t.Fatalf("checkpoint was not saved: %#v, %v", saved.Context, err)
	}
}

func TestTUICompactCommandNoHistoryAndFailureKeepContext(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 4000
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, &fakeClient{})
	m.input.SetValue("/compact")
	updated, _ := m.submitChat()
	m = updated.(model)
	if m.status != "Nothing to compact" || m.waiting {
		t.Fatalf("empty command: status=%q waiting=%v", m.status, m.waiting)
	}
	for _, message := range compactCommandHistory() {
		m.messages = append(m.messages, message)
		m.memory.Append(message)
	}
	before := m.memory.Messages()
	m.input.SetValue("/compact")
	updated, _ = m.submitChat()
	m = updated.(model)
	updated, _ = m.Update(compactionResultMsg{turnID: m.turnID, manual: true, err: errors.New("checkpoint failed")})
	m = updated.(model)
	if m.waiting || m.compacting || m.turnCancel != nil || m.input.Value() != "" || len(m.messages) != len(before) {
		t.Fatalf("failed manual compaction changed turn or transcript: status=%q", m.status)
	}
	if got := m.memory.Messages(); len(got) != len(before) || hasMessageNamed(got, memory.SummaryName) {
		t.Fatalf("failed manual compaction changed context: %#v", got)
	}
}

func TestACPCompactCommandPreservesTranscriptWithoutChatTurn(t *testing.T) {
	fake := &fakeClient{}
	agent, store, connection := testACPAgent(t, fake, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	value := runtime.state.activeConfig()
	value.Provider.ContextWindow = 8000
	runtime.state.config = value
	runtime.state.memory.Configure(memoryPolicy(value))
	for _, message := range compactCommandHistory() {
		runtime.state.messages = append(runtime.state.messages, message)
		runtime.state.memory.Append(message)
	}
	if runtime.state.memory.ShouldCompact() {
		t.Fatal("test history must be below the automatic compaction trigger")
	}
	before := len(runtime.state.messages)
	runtime.state.conversationID = "previous-conversation"
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/compact")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(fake.requests) != 1 || !isCheckpointRequestForTest(fake.requests[0]) {
		t.Fatalf("ACP command = %#v, requests = %#v", response, fake.requests)
	}
	if len(runtime.state.messages) != before || !hasMessageNamed(runtime.state.memory.Messages(), memory.SummaryName) || runtime.state.conversationID != "" {
		t.Fatalf("ACP compact changed transcript or missed checkpoint: transcript=%d context=%#v", len(runtime.state.messages), runtime.state.memory.Messages())
	}
	saved, err := activeACPWorkspaceStore(t, agent, sessionID).Load()
	if err != nil || !hasMessageNamed(saved.Context, memory.SummaryName) {
		t.Fatalf("ACP checkpoint was not saved: %#v, %v", saved.Context, err)
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if !strings.Contains(output, "Context compacted") {
		t.Fatalf("ACP compact output = %q", output)
	}
}

func TestACPCompactCommandNoHistory(t *testing.T) {
	fake := &fakeClient{}
	agent, store, connection := testACPAgent(t, fake, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	value := runtime.state.activeConfig()
	value.Provider.ContextWindow = 4000
	runtime.state.config = value
	runtime.state.memory.Configure(memoryPolicy(value))
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/compact")},
	})
	if err != nil || response.StopReason != acp.StopReasonEndTurn || len(fake.requests) != 0 {
		t.Fatalf("ACP empty compact: response=%#v err=%v requests=%d", response, err, len(fake.requests))
	}
	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if !strings.Contains(output, "Nothing to compact") {
		t.Fatalf("ACP empty compact output = %q", output)
	}
}

func TestACPCompactCommandFailureKeepsContext(t *testing.T) {
	failing := &compactingLoopClient{compactionErr: errors.New("checkpoint failed")}
	agent, store, _ := testACPAgent(t, failing, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	value := runtime.state.activeConfig()
	value.Provider.ContextWindow = 8000
	runtime.state.config = value
	runtime.state.memory.Configure(memoryPolicy(value))
	for _, message := range compactCommandHistory() {
		runtime.state.messages = append(runtime.state.messages, message)
		runtime.state.memory.Append(message)
	}
	before := runtime.state.memory.Messages()
	runtime.state.conversationID = "previous-conversation"
	_, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/compact")},
	})
	if err == nil || !strings.Contains(err.Error(), "checkpoint failed") || failing.compactCalls != 1 || failing.normalCalls != 0 {
		t.Fatalf("ACP failure: err=%v compact=%d normal=%d", err, failing.compactCalls, failing.normalCalls)
	}
	if got := runtime.state.memory.Messages(); len(got) != len(before) || hasMessageNamed(got, memory.SummaryName) || runtime.state.conversationID != "previous-conversation" {
		t.Fatalf("failed ACP compaction changed context: %#v", got)
	}
}

func TestTUICompactCommandCancelledResultIsIgnored(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 4000
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	for _, message := range compactCommandHistory() {
		m.messages = append(m.messages, message)
		m.memory.Append(message)
	}
	before := m.memory.Messages()
	m.conversationID = "previous-conversation"
	m.input.SetValue("/compact")
	updated, command := m.submitChat()
	m = updated.(model)
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatal("manual compaction did not start")
	}
	var result compactionResultMsg
	for _, child := range batch {
		if message, ok := child().(compactionResultMsg); ok {
			result = message
		}
	}
	if !result.manual {
		t.Fatal("manual compaction result missing")
	}
	updated, _ = m.interruptTurn()
	m = updated.(model)
	updated, _ = m.Update(result)
	m = updated.(model)
	if m.waiting || m.conversationID != "previous-conversation" || hasMessageNamed(m.memory.Messages(), memory.SummaryName) || len(m.memory.Messages()) != len(before) {
		t.Fatalf("cancelled checkpoint changed context: waiting=%v conversation=%q context=%#v", m.waiting, m.conversationID, m.memory.Messages())
	}
}

func TestTUICompactCommandSaveFailureKeepsContext(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 4000
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	for _, message := range compactCommandHistory() {
		m.messages = append(m.messages, message)
		m.memory.Append(message)
	}
	before := m.memory.Messages()
	m.conversationID = "previous-conversation"
	blockedRoot := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blockedRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.workspaceStore = &workspace.Store{Root: blockedRoot}
	m.input.SetValue("/compact")
	updated, command := m.submitChat()
	m = updated.(model)
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatal("manual compaction did not start")
	}
	var result compactionResultMsg
	for _, child := range batch {
		if message, ok := child().(compactionResultMsg); ok {
			result = message
		}
	}
	if !result.manual || result.err != nil {
		t.Fatalf("checkpoint request failed before save: %#v", result)
	}
	updated, _ = m.Update(result)
	m = updated.(model)
	if m.waiting || m.compacting || m.turnCancel != nil || m.conversationID != "previous-conversation" || m.compactionTarget != 0 || !strings.Contains(m.status, "compact context:") {
		t.Fatalf("save failure changed TUI state: waiting=%v conversation=%q status=%q", m.waiting, m.conversationID, m.status)
	}
	if got := m.memory.Messages(); len(got) != len(before) || hasMessageNamed(got, memory.SummaryName) || m.memory.Stats().Compactions != 0 {
		t.Fatalf("save failure changed TUI context: %#v", got)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("save failure sent another chat request: %d", len(fake.requests))
	}
}

func TestACPCompactCommandSaveFailureKeepsContext(t *testing.T) {
	fake := &fakeClient{}
	agent, store, _ := testACPAgent(t, fake, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	value := runtime.state.activeConfig()
	value.Provider.ContextWindow = 8000
	runtime.state.config = value
	runtime.state.memory.Configure(memoryPolicy(value))
	for _, message := range compactCommandHistory() {
		runtime.state.messages = append(runtime.state.messages, message)
		runtime.state.memory.Append(message)
	}
	before := runtime.state.memory.Messages()
	runtime.state.conversationID = "previous-conversation"
	blockedRoot := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blockedRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.state.workspaceStore = &workspace.Store{Root: blockedRoot, SessionID: string(sessionID)}
	_, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/compact")},
	})
	if err == nil || len(fake.requests) != 1 || !isCheckpointRequestForTest(fake.requests[0]) {
		t.Fatalf("ACP save failure: err=%v requests=%#v", err, fake.requests)
	}
	if got := runtime.state.memory.Messages(); len(got) != len(before) || hasMessageNamed(got, memory.SummaryName) || runtime.state.memory.Stats().Compactions != 0 || runtime.state.conversationID != "previous-conversation" {
		t.Fatalf("save failure changed ACP context: %#v, conversation=%q", got, runtime.state.conversationID)
	}
}

type cancelAfterCheckpointClient struct {
	fakeClient
	cancel context.CancelFunc
}

func (c *cancelAfterCheckpointClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	response, err := c.fakeClient.Chat(ctx, request)
	c.cancel()
	return response, err
}

func TestACPCompactCommandCancelledProviderResultKeepsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake := &cancelAfterCheckpointClient{cancel: cancel}
	agent, store, _ := testACPAgent(t, fake, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	value := runtime.state.activeConfig()
	value.Provider.ContextWindow = 8000
	runtime.state.config = value
	runtime.state.memory.Configure(memoryPolicy(value))
	for _, message := range compactCommandHistory() {
		runtime.state.messages = append(runtime.state.messages, message)
		runtime.state.memory.Append(message)
	}
	before := runtime.state.memory.Messages()
	runtime.state.conversationID = "previous-conversation"
	if err := runtime.compactContext(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled checkpoint error = %v", err)
	}
	if len(fake.requests) != 1 || !isCheckpointRequestForTest(fake.requests[0]) {
		t.Fatalf("checkpoint request = %#v", fake.requests)
	}
	if got := runtime.state.memory.Messages(); len(got) != len(before) || hasMessageNamed(got, memory.SummaryName) || runtime.state.memory.Stats().Compactions != 0 || runtime.state.conversationID != "previous-conversation" {
		t.Fatalf("cancelled ACP checkpoint changed context: %#v, conversation=%q", got, runtime.state.conversationID)
	}
}
