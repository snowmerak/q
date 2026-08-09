package subagent

import (
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	TraceAssistant  = "assistant"
	TraceToolCall   = "tool_call"
	TraceToolResult = "tool_result"
)

// TraceEvent is the inspectable, model-visible execution transcript projected
// to the TUI. It includes assistant text and tool traffic, but never fabricates
// or exposes hidden chain-of-thought that the provider did not return.
type TraceEvent struct {
	Agent    string
	TaskID   string
	ParentID string
	Kind     string
	Name     string
	Content  string
	IsError  bool
}

type TraceFunc func(TraceEvent)

func reportTrace(trace TraceFunc, event TraceEvent) {
	if trace == nil {
		return
	}
	event.Agent = strings.TrimSpace(event.Agent)
	event.TaskID = strings.TrimSpace(event.TaskID)
	event.ParentID = strings.TrimSpace(event.ParentID)
	event.Kind = strings.TrimSpace(event.Kind)
	event.Name = strings.TrimSpace(event.Name)
	event.Content = strings.TrimSpace(event.Content)
	if event.Agent == "" || event.Kind == "" {
		return
	}
	trace(event)
}

func traceAssistant(trace TraceFunc, agent, taskID, parentID string, message client.Message) {
	if content := strings.TrimSpace(message.TextContent()); content != "" {
		reportTrace(trace, TraceEvent{
			Agent: agent, TaskID: taskID, ParentID: parentID,
			Kind: TraceAssistant, Content: content,
		})
	}
	for _, call := range message.ToolCalls {
		reportTrace(trace, TraceEvent{
			Agent: agent, TaskID: taskID, ParentID: parentID,
			Kind: TraceToolCall, Name: call.Function.Name, Content: call.Function.Arguments,
		})
	}
}

func traceToolResult(trace TraceFunc, agent, taskID, parentID, name string, result client.ToolResult) {
	reportTrace(trace, TraceEvent{
		Agent: agent, TaskID: taskID, ParentID: parentID,
		Kind: TraceToolResult, Name: name, Content: result.Content, IsError: result.IsError,
	})
}
