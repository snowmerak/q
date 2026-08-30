package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func acpTraceContentText(content []acp.ToolCallContent) string {
	var result string
	for _, block := range content {
		if block.Content != nil && block.Content.Content.Text != nil {
			result += block.Content.Content.Text.Text + "\n"
		}
	}
	return result
}

func TestACPPlanTraceKeepsRepeatedAndConcurrentCallsSeparate(t *testing.T) {
	for _, callID := range []string{"reused-call-id", ""} {
		t.Run("callID="+callID, func(t *testing.T) {
			root := t.TempDir()
			var updates []acp.SessionUpdate
			projector := newACPPlanTrace(root, "plan-1", func(_ context.Context, update acp.SessionUpdate) error {
				updates = append(updates, update)
				return nil
			})
			first := agentTrace{Agent: "scout", TaskID: "task-a", ParentID: "griller-a", CallID: callID,
				Kind: subagent.TraceToolCall, Name: "read_file", Content: " \n{\"path\":\"main.go\",\"offset\":1}\n "}
			second := first
			second.Content = `{"path":"main.go","offset":10}`
			other := first
			other.TaskID = "task-b"
			other.Content = `{"path":"other.go"}`
			for _, event := range []agentTrace{first, second, other} {
				if err := projector.handle(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			other.Kind, other.Content = subagent.TraceToolResult, `{"result":"other task"}`
			first.Kind, first.Content, first.IsError = subagent.TraceToolResult, " \ninvalid range\n ", true
			second.Kind, second.Content = subagent.TraceToolResult, `{"loom_ref":"loom://0123456789abcdef0123456789abcdef","preview":"package main"}`
			for _, event := range []agentTrace{other, first, second} {
				if err := projector.handle(t.Context(), event); err != nil {
					t.Fatal(err)
				}
			}
			if len(updates) != 6 || len(projector.pending) != 0 {
				t.Fatalf("updates=%#v, pending=%#v", updates, projector.pending)
			}
			ids := make(map[acp.ToolCallId]bool)
			for _, update := range updates[:3] {
				call := update.ToolCall
				if call == nil || ids[call.ToolCallId] || call.Status != acp.ToolCallStatusInProgress || call.Kind != acp.ToolKindRead {
					t.Fatalf("invalid start = %#v", call)
				}
				ids[call.ToolCallId] = true
				if !strings.Contains(call.Title, "scout [task-") || !strings.Contains(call.Title, ".go") {
					t.Fatalf("title = %q", call.Title)
				}
				meta := call.Meta["q"].(map[string]any)
				if meta["callId"] != callID || meta["parentTaskId"] != "griller-a" {
					t.Fatalf("metadata = %#v", meta)
				}
			}
			if locations := updates[0].ToolCall.Locations; len(locations) != 1 || locations[0].Path != filepath.Join(root, "main.go") {
				t.Fatalf("read locations = %#v", locations)
			}
			if input := updates[0].ToolCall.RawInput.(map[string]any); input["path"] != "main.go" || input["offset"] != float64(1) {
				t.Fatalf("raw input = %#v", input)
			}
			for i, startIndex := range []int{2, 0, 1} {
				start, end := updates[startIndex].ToolCall, updates[i+3].ToolCallUpdate
				if end == nil || end.ToolCallId != start.ToolCallId || end.Status == nil {
					t.Fatalf("result attached to wrong call: %#v", end)
				}
				wantStatus := acp.ToolCallStatusCompleted
				if startIndex == 0 {
					wantStatus = acp.ToolCallStatusFailed
				}
				if *end.Status != wantStatus || !strings.HasPrefix(acpTraceContentText(end.Content), acpTraceContentText(start.Content)) {
					t.Fatalf("result lost input or status: %#v", end)
				}
			}
			if output := updates[4].ToolCallUpdate; output.RawOutput != first.Content || !strings.Contains(acpTraceContentText(output.Content), first.Content) {
				t.Fatalf("plain text error was changed: %#v", output)
			}
			if output := updates[5].ToolCallUpdate.RawOutput.(map[string]any); output["preview"] != "package main" || output["loom_ref"] == "" {
				t.Fatalf("Loom receipt lost: %#v", output)
			}
			// Reusing an ID after a result must also create a new ACP call.
			first.Kind = subagent.TraceToolCall
			if err := projector.handle(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			if ids[updates[6].ToolCall.ToolCallId] {
				t.Fatal("reused a completed ACP call ID")
			}
		})
	}
}

func TestACPPlanTraceHandlesPartialTracesAndInterruptedCalls(t *testing.T) {
	var updates []acp.SessionUpdate
	emit := func(_ context.Context, update acp.SessionUpdate) error {
		updates = append(updates, update)
		return nil
	}
	projector := newACPPlanTrace(t.TempDir(), "partial", emit)
	for _, event := range []agentTrace{
		{Agent: "scout", TaskID: "task-a", Kind: subagent.TraceAssistant, Content: "Checking the requested line range."},
		{Agent: "scout", TaskID: "task-a", CallID: "orphan", Kind: subagent.TraceToolResult, Name: "read_file", Content: ""},
		{Agent: "scout", TaskID: "task-a", CallID: "interrupted", Kind: subagent.TraceToolCall, Name: "read_file", Content: `{"path":"main.go"}`},
	} {
		if err := projector.handle(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := projector.finishPending(t.Context(), "Plan cancelled"); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 5 || len(projector.pending) != 0 {
		t.Fatalf("updates=%#v, pending=%#v", updates, projector.pending)
	}
	if thought := updates[0].AgentThoughtChunk; thought == nil || !strings.Contains(thought.Content.Text.Text, "scout [task-a]") {
		t.Fatalf("assistant trace = %#v", updates[0])
	}
	if updates[1].ToolCall.ToolCallId != updates[2].ToolCallUpdate.ToolCallId || *updates[2].ToolCallUpdate.Status != acp.ToolCallStatusCompleted {
		t.Fatal("partial result did not receive a complete lifecycle")
	}
	end := updates[4].ToolCallUpdate
	if end.ToolCallId != updates[3].ToolCall.ToolCallId || *end.Status != acp.ToolCallStatusFailed ||
		end.RawOutput != "Plan cancelled" || !strings.Contains(acpTraceContentText(end.Content), `{"path":"main.go"}`) {
		t.Fatalf("interrupted call = %#v", end)
	}
	for _, update := range updates {
		if update.AgentMessageChunk != nil {
			t.Fatal("trace polluted the final answer")
		}
		// Exercise the SDK's actual tagged-union wire encoding.
		body, err := json.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		var decoded acp.SessionUpdate
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("invalid ACP update %s: %v", body, err)
		}
	}
}

func TestACPPlanTracePropagatesTransportErrors(t *testing.T) {
	want := errors.New("client disconnected")
	projector := newACPPlanTrace(t.TempDir(), "failed", func(context.Context, acp.SessionUpdate) error { return want })
	if err := projector.handle(t.Context(), agentTrace{Agent: "scout", Kind: subagent.TraceToolCall, Name: "read_file"}); !errors.Is(err, want) {
		t.Fatalf("start error = %v", err)
	}
	if err := projector.finishPending(t.Context(), "Plan stopped"); !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v", err)
	}
}

type acpBlockingScoutTraceTools struct {
	acpScoutTraceTools
	started chan struct{}
}

func (f *acpBlockingScoutTraceTools) Call(ctx context.Context, _ client.ToolCall) (client.ToolResult, error) {
	close(f.started)
	<-ctx.Done()
	return client.ToolResult{}, ctx.Err()
}

type acpContextAwareTraceConnection struct{ *fakeACPConnection }

func (c *acpContextAwareTraceConnection) SessionUpdate(ctx context.Context, update acp.SessionNotification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.fakeACPConnection.SessionUpdate(ctx, update)
}

func TestACPAgentCancelsActiveScoutToolCards(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.DelegateScoutToolName, `{"objective":"Inspect main.go"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("read_file", `{"path":"main.go"}`)}},
	}}
	tools := &acpBlockingScoutTraceTools{started: make(chan struct{})}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, tools)
	agent.setConnection(&acpContextAwareTraceConnection{connection})
	agent.state.config.Provider.Model = "plan-model"
	if _, err := agent.Initialize(t.Context(), acp.InitializeRequest{ClientCapabilities: acp.ClientCapabilities{
		Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
	}}); err != nil {
		t.Fatal(err)
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	type outcome struct {
		response acp.PromptResponse
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/plan inspect main.go")}})
		done <- outcome{response, err}
	}()
	select {
	case <-tools.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Scout read did not start")
	}
	if err := agent.Cancel(t.Context(), acp.CancelNotification{SessionId: sessionID}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.response.StopReason != acp.StopReasonCancelled {
			t.Fatalf("cancelled response = %#v, error = %v", result.response, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Scout cancellation did not finish")
	}
	assertACPTraceLifecycles(t, connection.snapshot(), 2, 2)
}

type acpScoutTraceTools struct{ fakeAgentTools }

func (f *acpScoutTraceTools) Tools() []client.Tool {
	return append(f.fakeAgentTools.Tools(), client.Tool{Type: client.ToolTypeFunction,
		Function: client.FunctionDefinition{Name: "read_file", Parameters: map[string]any{"type": "object"}},
	})
}

func (f *acpScoutTraceTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	f.calls = append(f.calls, call)
	if len(f.calls) == 1 {
		return client.ToolResult{}, errors.New("requested range starts after end of file")
	}
	return client.ToolResult{Content: `{"loom_ref":"loom://0123456789abcdef0123456789abcdef","preview":"package main","result":{"path":"main.go"}}`}, nil
}

func TestACPAgentStreamsScoutInputsResultsAndCompletion(t *testing.T) {
	configuredClient := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.DelegateScoutToolName, `{"objective":"Inspect main.go"}`)}},
		{Role: client.RoleAssistant, Content: "Checking the requested line range.", ToolCalls: []client.ToolCall{planToolCall("read_file", `{"path":"main.go","offset":100}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("read_file", `{"path":"main.go","offset":1}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ScoutCompleteToolName, `{"outcome":"succeeded","summary":"Located main.go"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitBriefToolName, `{"objective":"Inspect main.go","conditions":["Preserve main.go"],"acceptance_criteria":["Inspect entry point"]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.SubmitPlanToolName, `{"outcome":"succeeded","summary":"Verify main.go","conditions":["Preserve main.go"],"steps":[{"title":"Verify entry point","description":"Inspect main.go","target":{"any":[{"all":[{"kind":"paths","paths":["main.go"]}]}]}}],"verification":["Inspect entry point"]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.CoderCompleteToolName, `{"outcome":"succeeded","summary":"Verified main.go","verification":["Inspected entry point"]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.ReviewTaskToolName, `{"decision":"next","feedback":"","fact_changes":[{"op":"add","value":"Entry point inspected","reason":"the task inspected the entry point"}]}`)}},
	}}
	tools := &acpScoutTraceTools{}
	agent, workspaceStore, connection := testACPAgent(t, configuredClient, tools)
	agent.state.config.Provider.Model = "plan-model"
	connection.answer = "approve"
	if _, err := agent.Initialize(t.Context(), acp.InitializeRequest{ClientCapabilities: acp.ClientCapabilities{
		Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
	}}); err != nil {
		t.Fatal(err)
	}
	sessionID := openTestACPSession(t, agent, workspaceStore.Root)
	response, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock("/plan inspect main.go")}})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acp.StopReasonEndTurn || len(configuredClient.requests) != 8 || len(tools.calls) != 2 {
		t.Fatalf("response=%#v, requests=%d, tools=%#v", response, len(configuredClient.requests), tools.calls)
	}
	assertACPTraceLifecycles(t, connection.snapshot(), 8, 1)
	var readIDs []acp.ToolCallId
	var sawScoutReport, sawDelegateReport, sawAssistant bool
	for _, notification := range connection.snapshot() {
		if start := notification.Update.ToolCall; start != nil {
			meta := start.Meta["q"].(map[string]any)
			if meta["agent"] == "scout" && meta["toolName"] == "read_file" {
				readIDs = append(readIDs, start.ToolCallId)
				if meta["taskId"] == "" || meta["parentTaskId"] == "" || meta["callId"] != "call-read_file" ||
					!strings.Contains(start.Title, "main.go") {
					t.Fatalf("unidentified Scout call: %#v", start)
				}
				if offset := start.RawInput.(map[string]any)["offset"]; offset != []float64{100, 1}[len(readIDs)-1] {
					t.Fatalf("read input = %#v", start.RawInput)
				}
			}
		}
		if end := notification.Update.ToolCallUpdate; end != nil {
			meta := end.Meta["q"].(map[string]any)
			content := acpTraceContentText(end.Content)
			if meta["agent"] == "scout" && meta["toolName"] == subagent.ScoutCompleteToolName {
				sawScoutReport = strings.Contains(content, "Located main.go")
			}
			if meta["toolName"] == subagent.DelegateScoutToolName {
				sawDelegateReport = strings.Contains(content, "Located main.go") && strings.Contains(content, "loom_ref")
			}
			if meta["toolName"] == "read_file" {
				if !strings.Contains(content, "Input:") || !strings.Contains(content, "main.go") ||
					(*end.Status == acp.ToolCallStatusFailed && !strings.Contains(content, "requested range starts after end of file")) ||
					(*end.Status == acp.ToolCallStatusCompleted && !strings.Contains(content, "package main")) {
					t.Fatalf("read result missing diagnostic content: %s", content)
				}
			}
		}
		if thought := notification.Update.AgentThoughtChunk; thought != nil && strings.Contains(thought.Content.Text.Text, "Checking the requested line range.") {
			sawAssistant = strings.Contains(thought.Content.Text.Text, "scout [")
		}
	}
	if len(readIDs) != 2 || readIDs[0] == readIDs[1] || !sawScoutReport || !sawDelegateReport || !sawAssistant {
		t.Fatalf("reads=%v, scout report=%v, delegate report=%v, assistant=%v", readIDs, sawScoutReport, sawDelegateReport, sawAssistant)
	}
	saved, err := activeACPWorkspaceStore(t, agent).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Transcript) != 2 {
		t.Fatalf("subagent trace polluted the main transcript: %#v", saved.Transcript)
	}
}

func assertACPTraceLifecycles(t *testing.T, notifications []acp.SessionNotification, wantCalls, wantFailures int) {
	t.Helper()
	started := make(map[acp.ToolCallId]bool)
	finished := make(map[acp.ToolCallId]bool)
	failures := 0
	for _, notification := range notifications {
		if call := notification.Update.ToolCall; call != nil {
			if started[call.ToolCallId] || call.Status != acp.ToolCallStatusInProgress {
				t.Fatalf("duplicate or invalid start: %#v", call)
			}
			started[call.ToolCallId] = true
		}
		if call := notification.Update.ToolCallUpdate; call != nil {
			if !started[call.ToolCallId] || finished[call.ToolCallId] || call.Status == nil ||
				(*call.Status != acp.ToolCallStatusCompleted && *call.Status != acp.ToolCallStatusFailed) {
				t.Fatalf("unmatched or nonterminal result: %#v", call)
			}
			finished[call.ToolCallId] = true
			if *call.Status == acp.ToolCallStatusFailed {
				failures++
			}
		}
	}
	if len(started) != wantCalls || len(finished) != wantCalls || failures != wantFailures {
		t.Fatalf("tool lifecycles: started=%d finished=%d failed=%d; want %d calls, %d failures", len(started), len(finished), failures, wantCalls, wantFailures)
	}
}
