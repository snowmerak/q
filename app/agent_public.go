package app

import (
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

// AgentLoopRequest contains the host-owned dependencies and conversation state
// used by RunAgentLoop. RunAgentLoop copies Messages and does not close Client or
// Tools.
type AgentLoopRequest = agentloop.Request

// AgentLoopResult is the terminal model response and loop accounting data.
type AgentLoopResult = agentloop.Result

// WorkspaceMessageOptions controls the Q workspace instructions appended by
// PrepareWorkspaceMessages.
type WorkspaceMessageOptions = agentloop.WorkspaceMessageOptions

// Status reports a transient loop status.
func (e AgentEvent) Status() (string, bool) {
	return e.status, e.status != ""
}

// Message reports a message appended to the loop transcript.
func (e AgentEvent) Message() (client.Message, bool) {
	if e.message == nil {
		return client.Message{}, false
	}
	return *e.message, true
}

// MessageIsToolError reports whether Message is a failed tool result.
func (e AgentEvent) MessageIsToolError() bool {
	return e.toolIsError
}

// ToolCall reports a tool call about to be handled by the loop.
func (e AgentEvent) ToolCall() (client.ToolCall, bool) {
	if e.call == nil {
		return client.ToolCall{}, false
	}
	return *e.call, true
}

// Question reports an interactive question and the channel used to resume the
// loop. The host must send exactly one AgentAnswer when it accepts the question.
func (e AgentEvent) Question() (AgentQuestion, chan<- AgentAnswer, bool) {
	if e.question == nil || e.answer == nil {
		return AgentQuestion{}, nil, false
	}
	return *e.question, e.answer, true
}

// Compaction reports a context checkpoint for optional host persistence.
func (e AgentEvent) Compaction() (AgentContextCompaction, bool) {
	if e.compaction == nil {
		return AgentContextCompaction{}, false
	}
	return *e.compaction, true
}

// ContextReplacement reports a repaired message that the host should use when
// it persists the transcript.
func (e AgentEvent) ContextReplacement() (AgentContextReplacement, bool) {
	if e.contextReplace == nil {
		return AgentContextReplacement{}, false
	}
	return *e.contextReplace, true
}

// StreamDelta reports a partial thinking or response fragment.
func (e AgentEvent) StreamDelta() (AgentStreamDelta, bool) {
	if e.streamDelta == nil {
		return AgentStreamDelta{}, false
	}
	return *e.streamDelta, true
}

// TaskStarted reports the task lifecycle opened by task_start.
func (e AgentEvent) TaskStarted() (workspace.ActiveTask, bool) {
	if e.taskStarted == nil {
		return workspace.ActiveTask{}, false
	}
	return *e.taskStarted, true
}

// TaskCompleted reports a successful task_complete call.
func (e AgentEvent) TaskCompleted() bool {
	return e.taskCompleted
}

// Result reports the terminal loop result. A terminal result may contain a nil
// response when the model returned no choices.
func (e AgentEvent) Result() (AgentLoopResult, bool) {
	if !e.complete {
		return AgentLoopResult{}, false
	}
	return AgentLoopResult{
		Response:        e.response,
		Outcome:         e.outcome,
		RequestEstimate: e.requestEstimate,
		ToolCalls:       e.toolCalls,
	}, true
}

// Err reports a terminal loop error.
func (e AgentEvent) Err() error {
	return e.err
}

// PrepareWorkspaceMessages appends Q's workspace and orchestration instructions.
func PrepareWorkspaceMessages(messages []client.Message, options WorkspaceMessageOptions) []client.Message {
	return agentloop.PrepareWorkspaceMessages(messages, options)
}
