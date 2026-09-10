package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestACPUsageUpdate(t *testing.T) {
	tests := []struct {
		name  string
		stats memory.Stats
		used  int
		size  int
		ok    bool
	}{
		{
			name:  "known context",
			stats: memory.Stats{PredictedTokens: 53_000, ContextWindow: 200_000},
			used:  53_000,
			size:  200_000,
			ok:    true,
		},
		{
			name:  "unknown context",
			stats: memory.Stats{PredictedTokens: 53_000},
		},
		{
			name:  "negative usage",
			stats: memory.Stats{PredictedTokens: -1, ContextWindow: 200_000},
			size:  200_000,
			ok:    true,
		},
		{
			name:  "usage over context window",
			stats: memory.Stats{PredictedTokens: 210_000, ContextWindow: 200_000},
			used:  210_000,
			size:  200_000,
			ok:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			update, ok := acpUsageUpdate(test.stats)
			if ok != test.ok {
				t.Fatalf("available = %t, want %t", ok, test.ok)
			}
			if !ok {
				if update.UsageUpdate != nil {
					t.Fatalf("usage update = %#v, want nil", update.UsageUpdate)
				}
				return
			}
			if update.UsageUpdate == nil {
				t.Fatal("usage update is nil")
			}
			if update.UsageUpdate.Used != test.used || update.UsageUpdate.Size != test.size {
				t.Fatalf("usage = %d/%d, want %d/%d", update.UsageUpdate.Used, update.UsageUpdate.Size, test.used, test.size)
			}
			if update.UsageUpdate.Cost != nil {
				t.Fatalf("cost = %#v, want nil", update.UsageUpdate.Cost)
			}
		})
	}
}

func TestACPUsageUpdateJSON(t *testing.T) {
	update, ok := acpUsageUpdate(memory.Stats{PredictedTokens: 53_000, ContextWindow: 200_000})
	if !ok {
		t.Fatal("usage update is unavailable")
	}
	body, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["sessionUpdate"] != "usage_update" || wire["used"] != float64(53_000) || wire["size"] != float64(200_000) {
		t.Fatalf("wire update = %s", body)
	}
	if _, found := wire["cost"]; found {
		t.Fatalf("wire update contains cost: %s", body)
	}
}

func TestACPUsagePublisherCoalescesPendingUpdate(t *testing.T) {
	first, _ := acpUsageUpdate(memory.Stats{PredictedTokens: 100, ContextWindow: 2_000})
	latest, _ := acpUsageUpdate(memory.Stats{PredictedTokens: 300, ContextWindow: 2_000})
	publisher := &acpUsagePublisher{updates: make(chan acp.SessionUpdate, 1)}
	publisher.publish(first)
	publisher.publish(latest)
	if len(publisher.updates) != 1 {
		t.Fatalf("pending updates = %d, want one", len(publisher.updates))
	}
	got := <-publisher.updates
	if got.UsageUpdate == nil || got.UsageUpdate.Used != 300 {
		t.Fatalf("pending usage = %#v, want latest", got.UsageUpdate)
	}
}

func TestACPUsageUpdateSkipsNilMemory(t *testing.T) {
	agent := &acpAgent{state: &model{ctx: t.Context()}}
	if update, ok := agent.currentUsageUpdate(); ok || update.UsageUpdate != nil {
		t.Fatalf("nil memory usage update = %#v, %t", update, ok)
	}
}

type stagedACPClient struct {
	started chan struct{}
	release chan struct{}
	usage   client.Usage
	once    sync.Once
}

func (c *stagedACPClient) Chat(ctx context.Context, _ client.ChatRequest) (*client.ChatResponse, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &client.ChatResponse{
		ConversationID: "conversation-1",
		Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: "reply",
		}}},
		Usage: c.usage,
	}, nil
}

func (*stagedACPClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (*stagedACPClient) Close() error                                       { return nil }

type observingACPUsageConnection struct {
	*fakeACPConnection
	usage chan acp.SessionNotification
}

type acpPromptResult struct {
	response acp.PromptResponse
	err      error
}

func (c *observingACPUsageConnection) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	if err := c.fakeACPConnection.SessionUpdate(ctx, notification); err != nil {
		return err
	}
	if notification.Update.UsageUpdate != nil {
		select {
		case c.usage <- notification:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func waitForUsage(
	t *testing.T,
	updates <-chan acp.SessionNotification,
	match func(*acp.SessionUsageUpdate) bool,
) acp.SessionNotification {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case update := <-updates:
			if usage := update.Update.UsageUpdate; usage != nil && match(usage) {
				return update
			}
		case <-timer.C:
			t.Fatal("timed out waiting for usage update")
		}
	}
}

func waitForPromptResult(t *testing.T, result <-chan acpPromptResult) acpPromptResult {
	t.Helper()
	select {
	case completed := <-result:
		return completed
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for prompt result")
		return acpPromptResult{}
	}
}

func TestACPAgentEmitsUsageAfterPrompt(t *testing.T) {
	configuredClient := &stagedACPClient{
		started: make(chan struct{}), release: make(chan struct{}),
		usage: client.Usage{PromptTokens: 120},
	}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.ContextWindow = 2_000
	agent.state.memory.Configure(memoryPolicy(agent.state.config))
	observed := &observingACPUsageConnection{fakeACPConnection: connection, usage: make(chan acp.SessionNotification, 16)}
	agent.setConnection(observed)

	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	if updates := usageNotifications(connection.snapshot()); len(updates) != 0 {
		t.Fatalf("session/new emitted %d usage updates, want none", len(updates))
	}
	before := activeACPRuntime(t, agent, sessionID).state.memory.Stats().PredictedTokens
	result := make(chan acpPromptResult, 1)
	go func() {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		result <- acpPromptResult{response: response, err: err}
	}()
	waitForSignal(t, configuredClient.started)
	first := waitForUsage(t, observed.usage, func(usage *acp.SessionUsageUpdate) bool {
		return usage.Used > before
	})
	close(configuredClient.release)
	completed := waitForPromptResult(t, result)
	if completed.err != nil {
		t.Fatal(completed.err)
	}
	response := completed.response
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", response.StopReason, acp.StopReasonEndTurn)
	}

	updates := usageNotifications(connection.snapshot())
	if len(updates) < 2 {
		t.Fatalf("prompt emitted %d usage updates, want an intermediate and final update", len(updates))
	}
	stats := activeACPRuntime(t, agent, sessionID).state.memory.Stats()
	got := updates[len(updates)-1]
	if got.SessionId != sessionID || got.Update.UsageUpdate == nil {
		t.Fatalf("usage notification = %#v", got)
	}
	if first.Update.UsageUpdate.Used >= got.Update.UsageUpdate.Used {
		t.Fatalf("usage did not increase after assistant response: first=%d final=%d", first.Update.UsageUpdate.Used, got.Update.UsageUpdate.Used)
	}
	if got.Update.UsageUpdate.Used != stats.PredictedTokens || got.Update.UsageUpdate.Size != stats.ContextWindow {
		t.Fatalf(
			"usage = %d/%d, want final memory %d/%d",
			got.Update.UsageUpdate.Used, got.Update.UsageUpdate.Size,
			stats.PredictedTokens, stats.ContextWindow,
		)
	}
}

type stagedCompactingACPClient struct {
	firstStarted      chan struct{}
	releaseFirst      chan struct{}
	compactionStarted chan struct{}
	releaseCompaction chan struct{}
	resumedStarted    chan struct{}
	releaseResumed    chan struct{}
	normalCalls       int
}

func (c *stagedCompactingACPClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if request.MaxCompletionTokens != nil && len(request.Tools) == 0 {
		close(c.compactionStarted)
		select {
		case <-c.releaseCompaction:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: testCheckpointJSON("condensed tool evidence"),
		}}}}, nil
	}

	c.normalCalls++
	if c.normalCalls == 1 {
		close(c.firstStarted)
		select {
		case <-c.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &client.ChatResponse{
			ConversationID: "before-compaction",
			Choices: []client.Choice{{Message: client.Message{
				Role: client.RoleAssistant,
				ToolCalls: []client.ToolCall{{
					ID: "large-read", Type: client.ToolTypeFunction,
					Function: client.FunctionCall{Name: "large_read", Arguments: `{}`},
				}},
			}}},
		}, nil
	}
	close(c.resumedStarted)
	select {
	case <-c.releaseResumed:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &client.ChatResponse{
		ConversationID: "after-compaction",
		Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: "done",
		}}},
	}, nil
}

func (*stagedCompactingACPClient) ListModels(context.Context) ([]client.Model, error) {
	return nil, nil
}
func (*stagedCompactingACPClient) Close() error { return nil }

func TestACPAgentEmitsCompactedMainLoopUsage(t *testing.T) {
	configuredClient := &stagedCompactingACPClient{
		firstStarted: make(chan struct{}), releaseFirst: make(chan struct{}),
		compactionStarted: make(chan struct{}), releaseCompaction: make(chan struct{}),
		resumedStarted: make(chan struct{}), releaseResumed: make(chan struct{}),
	}
	agent, workspaceStore, connection := testACPAgent(
		t,
		configuredClient,
		largeResultRuntime{content: strings.Repeat("large tool output ", 4_000)},
	)
	agent.state.config.Provider.ContextWindow = 16_000
	agent.state.memory.Configure(memoryPolicy(agent.state.config))
	observed := &observingACPUsageConnection{fakeACPConnection: connection, usage: make(chan acp.SessionNotification, 32)}
	agent.setConnection(observed)

	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	result := make(chan acpPromptResult, 1)
	go func() {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock("read the large result")},
		})
		result <- acpPromptResult{response: response, err: err}
	}()

	waitForSignal(t, configuredClient.firstStarted)
	initial := waitForUsage(t, observed.usage, func(*acp.SessionUsageUpdate) bool { return true })
	close(configuredClient.releaseFirst)
	waitForSignal(t, configuredClient.compactionStarted)
	high := waitForUsage(t, observed.usage, func(usage *acp.SessionUsageUpdate) bool {
		return usage.Used > initial.Update.UsageUpdate.Used+5_000
	})
	close(configuredClient.releaseCompaction)
	waitForSignal(t, configuredClient.resumedStarted)
	low := waitForUsage(t, observed.usage, func(usage *acp.SessionUsageUpdate) bool {
		return usage.Used < high.Update.UsageUpdate.Used
	})
	close(configuredClient.releaseResumed)
	completed := waitForPromptResult(t, result)
	if completed.err != nil {
		t.Fatal(completed.err)
	}
	response := completed.response
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", response.StopReason, acp.StopReasonEndTurn)
	}
	if low.Update.UsageUpdate.Used >= high.Update.UsageUpdate.Used {
		t.Fatalf("compaction usage = %d, want less than %d", low.Update.UsageUpdate.Used, high.Update.UsageUpdate.Used)
	}

	stats := activeACPRuntime(t, agent, sessionID).state.memory.Stats()
	if stats.Compactions != 1 {
		t.Fatalf("session compactions = %d, want one", stats.Compactions)
	}
	updates := usageNotifications(connection.snapshot())
	if len(updates) < 4 || updates[len(updates)-1].Update.UsageUpdate == nil {
		t.Fatalf("usage notifications = %#v", updates)
	}
	got := updates[len(updates)-1].Update.UsageUpdate
	if got.Used != stats.PredictedTokens || got.Size != stats.ContextWindow {
		t.Fatalf("usage = %d/%d, want compacted memory %d/%d", got.Used, got.Size, stats.PredictedTokens, stats.ContextWindow)
	}
}

func TestACPUsageUpdateJSONRPCWire(t *testing.T) {
	agent, workspaceStore, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	agent.state.config.Provider.ContextWindow = 2_000
	agent.state.memory.Configure(memoryPolicy(agent.state.config))

	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	closePipes := func() {
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = responseReader.Close()
		_ = responseWriter.Close()
	}
	t.Cleanup(closePipes)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stopClose := context.AfterFunc(ctx, closePipes)
	defer stopClose()
	connection := acp.NewAgentSideConnection(agent, responseWriter, requestReader)
	agent.setConnection(connection)
	encoder, decoder := json.NewEncoder(requestWriter), json.NewDecoder(responseReader)

	type envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	request := func(id int, method string, params any) {
		t.Helper()
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
	}
	readResponse := func(id string) envelope {
		t.Helper()
		var message envelope
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if string(message.ID) != id {
			t.Fatalf("response ID = %s, want %s", message.ID, id)
		}
		return message
	}

	request(1, "initialize", acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	readResponse("1")
	request(2, "session/new", acp.NewSessionRequest{Cwd: workspaceStore.Root, McpServers: []acp.McpServer{}})
	created := readResponse("2")
	var newSession acp.NewSessionResponse
	if err := json.Unmarshal(created.Result, &newSession); err != nil {
		t.Fatal(err)
	}

	request(3, "session/prompt", acp.PromptRequest{
		SessionId: newSession.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("hello over JSON-RPC")},
	})
	var usageWires []map[string]any
	var promptResponse envelope
	for len(promptResponse.ID) == 0 {
		var message envelope
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if message.Method == "session/update" {
			var notification struct {
				SessionID string         `json:"sessionId"`
				Update    map[string]any `json:"update"`
			}
			if err := json.Unmarshal(message.Params, &notification); err != nil {
				t.Fatal(err)
			}
			if notification.Update["sessionUpdate"] == "usage_update" {
				if notification.SessionID != string(newSession.SessionId) {
					t.Fatalf("usage session = %q, want %q", notification.SessionID, newSession.SessionId)
				}
				usageWires = append(usageWires, notification.Update)
			}
			continue
		}
		if string(message.ID) == "3" {
			promptResponse = message
		}
	}
	if len(usageWires) == 0 {
		t.Fatal("prompt response arrived without usage_update")
	}
	usageWire := usageWires[len(usageWires)-1]
	used, usedOK := usageWire["used"].(float64)
	size, sizeOK := usageWire["size"].(float64)
	if !usedOK || !sizeOK || used < 0 || size != float64(2_000) {
		t.Fatalf("wire usage = %#v", usageWire)
	}
	if _, found := usageWire["cost"]; found {
		t.Fatalf("wire usage contains cost: %#v", usageWire)
	}

	var response acp.PromptResponse
	if err := json.Unmarshal(promptResponse.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", response.StopReason, acp.StopReasonEndTurn)
	}
}

type failingACPUsageConnection struct {
	*fakeACPConnection
	err error
}

func (f *failingACPUsageConnection) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	if notification.Update.UsageUpdate != nil {
		return f.err
	}
	return f.fakeACPConnection.SessionUpdate(ctx, notification)
}

func TestACPUsageFailureDoesNotChangePromptResult(t *testing.T) {
	configuredClient := &fakeClient{}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.ContextWindow = 2_000
	agent.state.memory.Configure(memoryPolicy(agent.state.config))
	var logs bytes.Buffer
	agent.logger = slog.New(slog.NewTextHandler(&logs, nil))
	agent.setConnection(&failingACPUsageConnection{fakeACPConnection: connection, err: errors.New("usage unavailable")})

	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock("private prompt body")},
	})
	if err != nil {
		t.Fatalf("prompt failed because usage delivery failed: %v", err)
	}
	if response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", response.StopReason, acp.StopReasonEndTurn)
	}
	if !strings.Contains(logs.String(), "emit ACP session usage") ||
		!strings.Contains(logs.String(), "usage unavailable") ||
		!strings.Contains(logs.String(), "session_id="+string(sessionID)) {
		t.Fatalf("usage failure log = %q", logs.String())
	}
	if count := strings.Count(logs.String(), "emit ACP session usage"); count != 1 {
		t.Fatalf("usage failure logs = %d, want one: %q", count, logs.String())
	}
	if strings.Contains(logs.String(), "private prompt body") {
		t.Fatalf("usage failure log leaked prompt content: %q", logs.String())
	}
}

type blockingACPUsageConnection struct {
	*fakeACPConnection
	entered chan struct{}
	once    sync.Once
}

func (b *blockingACPUsageConnection) SessionUpdate(ctx context.Context, notification acp.SessionNotification) error {
	if notification.Update.UsageUpdate == nil {
		return b.fakeACPConnection.SessionUpdate(ctx, notification)
	}
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	return ctx.Err()
}

func TestACPUsageUpdateIsTimeBounded(t *testing.T) {
	configuredClient := &stagedACPClient{started: make(chan struct{}), release: make(chan struct{})}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, &fakeAgentTools{})
	agent.state.config.Provider.ContextWindow = 2_000
	agent.state.memory.Configure(memoryPolicy(agent.state.config))
	entered := make(chan struct{})
	agent.setConnection(&blockingACPUsageConnection{fakeACPConnection: connection, entered: entered})

	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	result := make(chan acpPromptResult, 1)
	go func() {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sessionID,
			Prompt:    []acp.ContentBlock{acp.TextBlock("hello")},
		})
		result <- acpPromptResult{response: response, err: err}
	}()
	waitForSignal(t, entered)
	waitForSignal(t, configuredClient.started)
	started := time.Now()
	close(configuredClient.release)
	completed := waitForPromptResult(t, result)
	if elapsed := time.Since(started); elapsed > 2*acpUsageUpdateTimeout {
		t.Fatalf("prompt remained blocked for %s", elapsed)
	}
	if completed.err != nil {
		t.Fatalf("prompt failed because usage client blocked: %v", completed.err)
	}
	if completed.response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", completed.response.StopReason, acp.StopReasonEndTurn)
	}
}

func usageNotifications(updates []acp.SessionNotification) []acp.SessionNotification {
	var usage []acp.SessionNotification
	for _, update := range updates {
		if update.Update.UsageUpdate != nil {
			usage = append(usage, update)
		}
	}
	return usage
}
