package subagent

import (
	"testing"

	"github.com/snowmerak/q/client"
)

func TestTracePreservesToolIdentityAndExactContent(t *testing.T) {
	var events []TraceEvent
	trace := func(event TraceEvent) { events = append(events, event) }
	call := scoutCall("read_file", " \n{\"path\":\"main.go\"}\n ")
	call.ID = "provider-call-42"
	traceAssistant(trace, "scout", "task-a", "griller-a", client.Message{
		Role: client.RoleAssistant, Content: "Inspecting the requested file.",
		ToolCalls: []client.ToolCall{call},
	})
	result := client.ToolResult{Content: " \tTool error: invalid range\n ", IsError: true}
	traceToolResult(trace, "scout", "task-a", "griller-a", call, result)
	if len(events) != 3 || events[0].Kind != TraceAssistant || events[1].Kind != TraceToolCall || events[2].Kind != TraceToolResult {
		t.Fatalf("trace events = %#v", events)
	}
	for _, event := range events[1:] {
		if event.Agent != "scout" || event.TaskID != "task-a" || event.ParentID != "griller-a" ||
			event.CallID != call.ID || event.Name != call.Function.Name {
			t.Fatalf("tool identity = %#v", event)
		}
	}
	if events[1].Content != call.Function.Arguments || events[2].Content != result.Content || !events[2].IsError {
		t.Fatalf("tool content changed: %#v", events)
	}
}
