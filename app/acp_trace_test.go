package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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
