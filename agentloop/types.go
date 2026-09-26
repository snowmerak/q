// Package agentloop runs Q's model and tool conversation loop without owning a
// provider process, user interface, or workspace session.
package agentloop

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

// ChatClient is the only model capability required by the loop. The embedding
// host owns the client's lifetime and closes it when applicable.
type ChatClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

// ToolRuntime supplies the tools and environment available to one loop.
type ToolRuntime interface {
	Tools() []client.Tool
	Environment() qtools.HostEnvironment
	Call(context.Context, client.ToolCall) (client.ToolResult, error)
}

type skillHintSearcher interface {
	SearchSkillHints(context.Context, string, int) (qtools.SkillHintSearchResult, error)
}

// Request contains host-owned dependencies and conversation state. RunAgentLoop
// copies Messages and does not close Client or Tools.
type Request struct {
	Client           ChatClient
	Tools            ToolRuntime
	Model            string
	ReasoningEffort  string
	Messages         []client.Message
	ConversationID   string
	WorkingDirectory string
	ActiveTask       *workspace.ActiveTask
	// RequireTaskAction prevents a started task from reporting success before
	// any non-orchestration tool has completed successfully.
	RequireTaskAction    bool
	PriorTaskAction      bool
	Stream               bool
	CoalesceInstructions bool
	ContextPolicy        memory.Policy
}

// Result is the terminal response and loop accounting data.
type Result struct {
	Response        *client.ChatResponse
	Outcome         string
	RequestEstimate int
	ToolCalls       int
}

// WorkspaceMessageOptions controls the instructions appended by
// PrepareWorkspaceMessages.
type WorkspaceMessageOptions struct {
	Root             string
	Tools            ToolRuntime
	ArchiveAvailable bool
}

// ContextReplacement repairs a message in the host's retained transcript.
type ContextReplacement struct {
	Index   int
	Message client.Message
}

// ErrInteractionUnavailable produces a recoverable tool error for an
// interactive question that the embedding host cannot answer.
var ErrInteractionUnavailable = errors.New("interactive input is unavailable in q remote mode")

var errRemoteInteractionUnavailable = ErrInteractionUnavailable

// Event reports progress and results from RunAgentLoop.
type Event struct {
	status          string
	message         *client.Message
	call            *client.ToolCall
	question        *AgentQuestion
	answer          chan AgentAnswer
	toolIsError     bool
	compaction      *AgentContextCompaction
	response        *client.ChatResponse
	complete        bool
	outcome         string
	requestEstimate int
	toolCalls       int
	learningName    string
	learningPayload json.RawMessage
	streamDelta     *AgentStreamDelta
	taskStarted     *workspace.ActiveTask
	taskCompleted   bool
	contextReplace  *ContextReplacement
	err             error
}

func (e Event) Status() (string, bool) { return e.status, e.status != "" }

func (e Event) Message() (client.Message, bool) {
	if e.message == nil {
		return client.Message{}, false
	}
	return *e.message, true
}

func (e Event) MessageIsToolError() bool { return e.toolIsError }

func (e Event) ToolCall() (client.ToolCall, bool) {
	if e.call == nil {
		return client.ToolCall{}, false
	}
	return *e.call, true
}

// Question returns the channel that must receive one AgentAnswer.
func (e Event) Question() (AgentQuestion, chan<- AgentAnswer, bool) {
	if e.question == nil || e.answer == nil {
		return AgentQuestion{}, nil, false
	}
	return *e.question, e.answer, true
}

func (e Event) Compaction() (AgentContextCompaction, bool) {
	if e.compaction == nil {
		return AgentContextCompaction{}, false
	}
	return *e.compaction, true
}

func (e Event) ContextReplacement() (ContextReplacement, bool) {
	if e.contextReplace == nil {
		return ContextReplacement{}, false
	}
	return *e.contextReplace, true
}

func (e Event) StreamDelta() (AgentStreamDelta, bool) {
	if e.streamDelta == nil {
		return AgentStreamDelta{}, false
	}
	return *e.streamDelta, true
}

func (e Event) TaskStarted() (workspace.ActiveTask, bool) {
	if e.taskStarted == nil {
		return workspace.ActiveTask{}, false
	}
	return *e.taskStarted, true
}

func (e Event) TaskCompleted() bool { return e.taskCompleted }

func (e Event) Learning() (string, json.RawMessage) {
	return e.learningName, append(json.RawMessage(nil), e.learningPayload...)
}

func (e Event) Result() (Result, bool) {
	if !e.complete {
		return Result{}, false
	}
	return Result{Response: e.response, Outcome: e.outcome, RequestEstimate: e.requestEstimate, ToolCalls: e.toolCalls}, true
}

func (e Event) Err() error { return e.err }

func toolAvailable(runtime ToolRuntime, name string) bool {
	if runtime == nil {
		return false
	}
	for _, tool := range runtime.Tools() {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
