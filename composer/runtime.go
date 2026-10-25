package composer

import (
	"context"
	"errors"
	"io"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type SessionRunner interface {
	RunWithOptions(context.Context, workspace.Store, string, string, app.SessionOptions, app.SessionEventSink) error
	ReleaseSession(workspace.Store, string) error
}

const instructions = `You are Composer, a conversational partner helping the user develop ideas.
Use the conversation so far to clarify the user's goals, audience, constraints, assumptions, and tradeoffs. Ask focused questions when they help; offer concrete alternatives and examples. Reflect decisions accurately and distinguish open questions from agreed details. Match the user's language. Let the user guide the pace: do not force a questionnaire or prematurely turn the discussion into an implementation task. You have no workspace tools. Do not claim to create files, execute commands, or start implementation.`

// Options uses Q's existing Agent Loop with an empty workspace tool catalog.
// The shared loop retains its orchestration and context-memory tools.
func Options(model, operation string) app.SessionOptions {
	return app.SessionOptions{
		Model: model, SystemPrompt: instructions, DisableLearning: true,
		RuntimeKey: "composer-conversation-v1", OperationID: operation,
		RuntimeFactory: func(context.Context, workspace.Store) (app.AgentToolRuntime, io.Closer, error) {
			return conversationRuntime{}, nil, nil
		},
	}
}

type conversationRuntime struct{}

func (conversationRuntime) Tools() []client.Tool               { return []client.Tool{} }
func (conversationRuntime) Environment() tools.HostEnvironment { return tools.HostEnvironment{} }
func (conversationRuntime) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	return client.ToolResult{}, errors.New("composer does not provide workspace tools")
}
