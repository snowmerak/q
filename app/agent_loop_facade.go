package app

import (
	"context"

	"github.com/snowmerak/q/agentloop"
)

// RunAgentLoop preserves the original app event contract while using the
// standalone agentloop package as the sole loop implementation.
func RunAgentLoop(ctx context.Context, request AgentLoopRequest, events chan<- AgentEvent) {
	runAgentLoop(ctx, request, events, false)
}

// runPersistedAgentLoop waits for the host to save each assistant tool turn
// and tool result before the core loop can dispatch the next tool. The core
// and UI use two event channels, so an ordinary unbuffered send is insufficient.
func runPersistedAgentLoop(ctx context.Context, request AgentLoopRequest, events chan<- AgentEvent) {
	runAgentLoop(ctx, request, events, true)
}

func runAgentLoop(ctx context.Context, request AgentLoopRequest, events chan<- AgentEvent, persisted bool) {
	if events == nil {
		return
	}
	defer close(events)
	source := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(ctx, request, source)
	for event := range source {
		projected := projectAgentLoopEvent(event)
		if persisted && projected.message != nil &&
			((projected.message.Role == "assistant" && len(projected.message.ToolCalls) > 0) || projected.message.Role == "tool") {
			projected.persistenceAck = make(chan struct{})
		}
		if !emitAgentEvent(ctx, events, projected) {
			return
		}
		if projected.persistenceAck != nil {
			select {
			case <-projected.persistenceAck:
			case <-ctx.Done():
				return
			}
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

func emitAgentEvent(ctx context.Context, events chan<- agentEvent, event agentEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
