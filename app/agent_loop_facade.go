package app

import (
	"context"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

// RunAgentLoop preserves the original app event contract while using the
// standalone agentloop package as the sole loop implementation.
func RunAgentLoop(ctx context.Context, request AgentLoopRequest, events chan<- AgentEvent) {
	if events == nil {
		return
	}
	defer close(events)
	source := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(ctx, request, source)
	for event := range source {
		if !emitAgentEvent(ctx, events, projectAgentLoopEvent(event)) {
			return
		}
	}
}

func projectAgentLoopEvent(source agentloop.Event) agentEvent {
	event := agentEvent{err: source.Err()}
	event.status, _ = source.Status()
	if message, ok := source.Message(); ok {
		event.message = &message
		event.toolIsError = source.MessageIsToolError()
	}
	if call, ok := source.ToolCall(); ok {
		event.call = &call
	}
	if question, answer, ok := source.Question(); ok {
		event.question = &question
		event.answer = answer
	}
	if compaction, ok := source.Compaction(); ok {
		event.compaction = &compaction
	}
	if replacement, ok := source.ContextReplacement(); ok {
		event.contextReplace = &replacement
	}
	if delta, ok := source.StreamDelta(); ok {
		event.streamDelta = &delta
	}
	if task, ok := source.TaskStarted(); ok {
		event.taskStarted = &task
	}
	event.taskCompleted = source.TaskCompleted()
	event.learningName, event.learningPayload = source.Learning()
	if result, ok := source.Result(); ok {
		event.response = result.Response
		event.complete = true
		event.outcome = result.Outcome
		event.requestEstimate = result.RequestEstimate
		event.toolCalls = result.ToolCalls
	}
	return event
}

func orchestrationToolResult(call client.ToolCall, content string, isError bool) client.Message {
	if isError {
		content = "Tool error: " + content
	}
	return client.Message{
		Role: client.RoleTool, Name: call.Function.Name,
		ToolCallID: call.ID, Content: content,
	}
}

func emitAgentEvent(ctx context.Context, events chan<- agentEvent, event agentEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
