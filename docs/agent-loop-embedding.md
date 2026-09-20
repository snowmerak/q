# Embedding q's agent loop in Go

The `github.com/snowmerak/q/agentloop` package exposes the model/tool loop used
by q's TUI, ACP, and remote hosts. An embedding application supplies the model,
tools, request context, and host callbacks while retaining ownership of
configuration, persistence, authorization, and dependency lifetimes.

## Add the module

```powershell
go get github.com/snowmerak/q@latest
```

Import the packages needed by the integration:

```go
import (
	"context"
	"fmt"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/tools"
)
```

Use a fixed module version in applications that require reproducible builds.
The public entry point is `agentloop.Run`; importing `app` is neither necessary
nor supported as an embedding boundary.

## Complete basic setup

The q model client and tool runtime already implement the required interfaces:

```go
func runTurn(
	ctx context.Context,
	workspaceRoot string,
	history []client.Message,
) (agentloop.Result, error) {
	model, err := client.FromEnvironment("gpt-5")
	if err != nil {
		return agentloop.Result{}, err
	}
	defer model.Close()

	runtime, err := tools.NewRuntime(ctx, workspaceRoot)
	if err != nil {
		return agentloop.Result{}, err
	}
	defer runtime.Close()

	return agentloop.Run(ctx, agentloop.Request{
		Client:           model,
		Tools:            runtime.ForRole(mcpconfig.RoleDefault),
		Messages:         history,
		Model:            "gpt-5",
		WorkingDirectory: workspaceRoot,
		ContextPolicy: memory.Policy{
			ContextWindow: 128_000,
			TriggerRatio:  .85,
			TargetRatio:   .22,
			RecentRatio:   .07,
		},
		MaxRounds: 64,
	}, agentloop.Hooks{})
}
```

`Run` is synchronous. Cancellation and deadlines come from `ctx`. It does not
close the model or runtime and does not write a session. A zero `MaxRounds`
keeps q's unbounded behavior; a positive limit is recommended at service
boundaries. A zero `ContextPolicy.ContextWindow` disables compaction.

## Public contracts

Only two capabilities are required:

```go
type ModelClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

type ToolRuntime interface {
	Tools() []client.Tool
	Call(context.Context, client.ToolCall) (client.ToolResult, error)
}
```

When `Request.Stream` is true, the loop uses `ChatStream` if the model also
implements `agentloop.StreamingModelClient`; otherwise it falls back to
`Chat`. Tool calls are dispatched sequentially. A `Call` error becomes a tool
error visible to the model so it can recover or report a blocker.

Custom OpenAI-compatible or local model clients therefore need only translate
q's `client.ChatRequest` and `client.ChatResponse` aliases. A custom runtime can
advertise any function schemas and route calls to in-process functions, RPC,
MCP, or another authorized execution layer.

The names `task_start`, `ask_to_user`, and `task_complete` are reserved. The
loop injects these orchestration tools itself and rejects a runtime catalog that
defines them.

## MCP tools

`tools.Runtime` uses the official Go MCP SDK for q's builtin MCP server.
`ConfigureExternal` adds stdio or Streamable HTTP MCP sessions. External tools
are assigned by role, so pass a role-scoped view to the loop:

```go
statuses := runtime.ConfigureExternal(ctx, workspaceRoot, mcpSettings)
for _, status := range statuses {
	if status.Error != "" {
		return fmt.Errorf("connect MCP server %s: %s", status.ID, status.Error)
	}
}

roleTools := runtime.ForRole(mcpconfig.RoleDefault)
result, err := agentloop.Run(ctx, agentloop.Request{
	Client: model,
	Tools:  roleTools,
	// ...
}, agentloop.Hooks{})
```

`RoleRuntime.Tools` includes builtin tools plus external tools assigned to the
selected role. `RoleRuntime.Call` checks that the requested name is in that
catalog before dispatching it. The parent `tools.Runtime` owns all MCP sessions
and must outlive every role view.

Automatic Agent Skill hints are optional host metadata. Supply
`Request.SkillHints` when the host has a search adapter implementing
`agentloop.SkillHintSearcher`; otherwise `search_skills` and `get_skill` remain
ordinary model-callable tools when present in the runtime catalog.

## Events and interactive questions

Events are delivered synchronously and in execution order. Returning an error
from the event callback aborts the run. Copy any mutable payload needed by an
asynchronous consumer before returning.

```go
hooks := agentloop.Hooks{
	Event: func(event agentloop.Event) error {
		switch event.Kind {
		case agentloop.EventMessage:
			return transcript.Append(event.Message)
		case agentloop.EventTaskStarted:
			return sessions.SaveActiveTask(event.Task)
		case agentloop.EventTaskCompleted:
			return sessions.ClearActiveTask()
		case agentloop.EventStreamDelta:
			ui.WriteDelta(event.Stream)
		}
		return nil
	},
	Ask: func(ctx context.Context, question agentloop.Question) (agentloop.Answer, error) {
		return ui.Ask(ctx, question)
	},
}
```

If `Ask` is nil, or returns `agentloop.ErrInteractionUnavailable`, the loop
returns an `ask_to_user` tool error to the model and lets it continue with the
available information. Any other error aborts `Run`.

Relevant event kinds are:

- `EventMessage` and `EventToolCall` for the transcript and activity UI.
- `EventStreamDelta` for reasoning or response text fragments.
- `EventContextReplace` when active-task or Agent Skill metadata augments the
  latest user message.
- `EventCompaction` when the compact request context receives a checkpoint.
- `EventTaskStarted` and `EventTaskCompleted` for durable task lifecycle state.

## Session ownership

Keep two concepts separate:

- The durable transcript is the host's user-visible audit history. Build it
  from the original input, ordered events, and terminal response.
- `Result.Context` is the possibly compacted model context to pass as
  `Request.Messages` on a later turn. It includes the final assistant message
  but may replace older evidence with a checkpoint.

Carry `Result.ConversationID` into the next request when the provider supports
stateful conversations. Persist `EventTaskStarted.Task` and pass it back as
`Request.ActiveTask` after a restart; clear it after `EventTaskCompleted`.
Hosts with a separate memory projection can apply `EventCompaction.Compaction`
instead of replacing their full transcript.

Do not close a model or runtime from an event callback. The component that
created each dependency owns it and should close it only after all active runs
have stopped.

## Integration checklist

- Choose a fixed q module version and a model identifier supported by the
  configured provider.
- Give each run a cancellable context and use a positive `MaxRounds` at service
  boundaries.
- Set `WorkingDirectory` to the authorized workspace root so nested workspace
  instructions resolve correctly.
- Scope MCP tools with `Runtime.ForRole`; do not advertise a tool that the host
  will refuse to execute.
- Persist ordered events before returning nil when event durability matters.
- Treat `Result.Context` as model context, not as a replacement for the audit
  transcript.
- Test direct replies, ordinary tool calls, `ask_to_user`, task completion,
  context compaction, cancellation, and runtime failures.

The downstream-style tests in
[`agentloop/loop_external_test.go`](../agentloop/loop_external_test.go) show a
custom `ModelClient` and `ToolRuntime` without importing q's application
package.
