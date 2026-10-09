# Embedding Q's Agent Loop in a Go application

Q exposes the same Agent Loop used by its TUI, ACP host, and internal Search/Web
Tester continuations through `github.com/snowmerak/q/agentloop`. An embedding
application supplies a model client, a workspace-scoped tool runtime, initial
messages, and an event consumer. The host retains ownership of configuration,
authorization, persistence, and dependency lifetimes.

`agentloop.RunAgentLoop` owns the single model/tool round implementation.
`app.RunAgentLoop` remains a compatibility facade for existing callers and
projects core events into the TUI/ACP event shape.

## Install and import

### Reuse an existing Q session

For Studio features that need Q's existing session behavior, use
`app.SessionHost.Run` or `RunWithOptions`. The host retains the live model and
tool connections and saves `workspace.Session`, including tool transcripts,
compact context, active tasks, native response replay and provider affinity.
Call the host again with the same store and session ID to continue the session.

`SessionOptions` can select a model and reasoning effort, append stable system
instructions, and separate the authorized checkout roots from the state root.
An injected `RuntimeFactory` supplies the complete authorized tool catalog and
its closer; the host does not add delegation or external tools to that runtime.
Give the policy a stable `RuntimeKey` so live sessions reopen when it changes.

For a retryable step, supply a stable `OperationID`. The operation is saved with
the actual session context. Retrying a pending operation continues that context
without inserting another copy of the prompt; retrying a completed operation
returns its saved result. A changed prompt requires a new operation ID.

Council uses one Q session per member and another for the chair for each council
turn. Reviews are subsequent user turns in those same member sessions. Session
files remain under the council directory, including
`~/.q/council/independent/<council-id>/.q/sessions/` for independent councils.
The council JSON contains session references and displayed results, while Q owns
the complete execution state. Older council turns without Q session references
can resume using their saved results as explicit input to new sessions; tool
history from those older runs was never stored and cannot be recovered.

Council ACP seats are run through `app.ParticipantRunner` using an enabled
`Agents.Connections` entry. Their Q session directory stores `council-acp.json`
with completed prompts and answers. A seat keeps its ACP process/session for the
current run. Reopening starts a fresh ACP session with completed exchanges as
explicit context, rather than depending on optional remote session restoration.
Completed operation IDs return saved answers; failed or interrupted prompts may
be attempted again, so this is not exactly-once execution at the external agent.
ACP seats reject mutating permission requests and do not advertise Q filesystem
or terminal access. External tools still belong to the agent; its own read-only
sandbox is required. ACP model, reasoning and cache settings remain agent-owned.

Council sets `SessionOptions.AnthropicPromptCache` to `"1h"` for member sessions
and `"off"` for the chair session. Q sends this as the Gateway-only
`q_anthropic_cache` Chat Completions field only when the selected provider is
native Anthropic. The policy applies to every model request in that session,
including tool rounds and context compaction. Other sessions retain the
Gateway's default cache behavior.

### Use the lower-level loop

Pin Q to the version your application has tested:

```powershell
go get github.com/snowmerak/q@<version>
```

The minimum standard integration uses these packages:

```go
import (
	"context"
	"fmt"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/tools"
)
```

Import `agentloop` for an embedding that does not need Q's Bubble Tea or ACP
host. It still uses Q's `tools` and `workspace` contracts. Existing callers
may continue importing `app` without changing their request or event handling.
`agentloop.Request.Extra` carries optional provider request fields through each
model and context-compaction call.

Ordinary tool calls in one assistant turn run concurrently in contiguous batches,
with at most four active calls per loop. Task lifecycle, user questions, memory
updates and workflow completion calls are serial barriers: preceding calls finish
and their results are recorded before the barrier runs; following calls start
only after its result is recorded. Results retain the assistant's original call
order, including during context compaction and session recovery.

Injected runtimes must support concurrent `Call` invocations. A batch does not
infer dependencies between ordinary tools, including commands and file changes;
dependent operations must be requested in separate model rounds. The loop joins
active batch workers before returning on cancellation, so runtimes should honor
the supplied context. An interrupted ordinary batch with unsaved results is
treated as having unknown outcomes rather than automatically replayed.

## Run a minimal workspace turn

Create the model client and tool runtime once for the lifetime chosen by the
host. `RunAgentLoop` does not close either dependency.

```go
func runTurn(ctx context.Context, root, prompt string) error {
	modelClient, err := client.FromEnvironment("gpt-5")
	if err != nil {
		return err
	}
	defer modelClient.Close()

	toolRuntime, err := tools.NewRuntime(ctx, root)
	if err != nil {
		return err
	}
	defer toolRuntime.Close()

	messages := agentloop.PrepareWorkspaceMessages(nil, agentloop.WorkspaceMessageOptions{
		Root:  root,
		Tools: toolRuntime,
	})
	messages = append(messages, client.Message{
		Role: client.RoleUser, Content: prompt,
	})

	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(ctx, agentloop.Request{
		Client:           modelClient,
		Tools:            toolRuntime,
		Model:            "gpt-5",
		Messages:         messages,
		WorkingDirectory: root,
	}, events)

	resultSeen := false
	for event := range events {
		if question, answers, ok := event.Question(); ok {
			fmt.Printf("input unavailable: %s\n", question.Question)
			answers <- agentloop.AgentAnswer{Err: agentloop.ErrInteractionUnavailable}
		}
		if call, ok := event.ToolCall(); ok {
			fmt.Printf("tool: %s\n", call.Function.Name)
		}
		if result, ok := event.Result(); ok {
			resultSeen = true
			if result.Response == nil || len(result.Response.Choices) == 0 {
				return fmt.Errorf("model returned no response choices")
			}
			fmt.Println(result.Response.Choices[0].Message.Content)
		}
		if err := event.Err(); err != nil {
			return err
		}
	}
	if !resultSeen {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("agent loop ended without a result")
	}
	return nil
}
```

`RunAgentLoop` is synchronous and owns the event channel: normally call it in
a goroutine, continuously drain the channel, and never close that channel from
the host. Cancellation and deadlines come from `ctx`. There is no separate
round-limit option, so externally reachable services should apply an
appropriate context deadline.

The loop requires both `Client` and `Tools`. A host that only needs one plain
completion can call the model client directly. A host that needs Q's loop
semantics for every model step can supply a non-nil runtime with an empty tool
catalog; the loop still provides its orchestration tools and response recovery.
Council uses this form for independent runs and a read-only runtime for
workspace runs.

## Enable Agent Skills without Archive or Library

`tools.NewRuntime` is the smallest builtin runtime. It does not advertise
`search_skills` or `get_skill`, because it has no searchable Skill index. To
enable local Agent Skills without starting the Workspace Archive, Session
Store, workspace memory, or Q Library, inject the smaller Skill-only store:

```go
skillStore := newMySkillStore() // implements tools.SkillStore

toolRuntime, err := tools.NewRuntimeWithSkillStore(ctx, root, skillStore)
if err != nil {
	return err
}
defer toolRuntime.Close()
defer skillStore.Close() // when the implementation has a Close method
```

`tools.SkillStore` is the following persistence boundary (expressed with
`sessionstore` record types so it can also be satisfied by Q's existing
stores):

```go
type SkillStore interface {
	Search(context.Context, sessionstore.SearchOptions) (sessionstore.SearchResult, error)
	Save(sessionstore.Record) (sessionstore.Record, error)
	Delete(string) error
}
```

At startup and refresh, the runtime discovers Agent Skills for the workspace
and reconciles their searchable metadata through `Search`, `Save`, and
`Delete`. A store may additionally implement `Prepare(context.Context,
[]sessionstore.Record)` to attach embeddings before records are saved. Skill
bodies remain in their source directories and `get_skill` reads them through
the existing registry, so the store does not need a `Get` method.

The injected store enables all three consumers of the same index:

- the model-visible `search_skills` tool;
- direct `get_skill` body loading for returned IDs;
- automatic Skill hints added by `RunAgentLoop` for user input, `task_start`,
  and answers returned from `ask_to_user`.

The Skill store does not enable `search_archive` or `get_archive_record`.
Conversely, existing `NewRuntimeWithArchive` constructors preserve their
behavior when the supplied archive also implements `tools.SkillStore`. The
caller owns the injected store and must close it separately when applicable.

## Prepare workspace context once

`PrepareWorkspaceMessages` copies the input slice and appends the pieces that
make a Q workspace behave like a Q workspace:

- root `AGENTS.md` instructions;
- host OS, architecture, shell, workspace root, and `.qignore` guidance;
- Agent Skill and proposition guidance when the corresponding tools exist;
- archive guidance only when `ArchiveAvailable` is true;
- the `task_start`, `ask_to_user`, and `task_complete` contract.

Call it when creating the initial context for a session. Do not run it again on
already prepared history, or the developer instructions will be duplicated.
Append each later user turn and the ordered loop events to the retained context.

Set `WorkingDirectory` to the same authorized root. The loop uses it to load
nested `AGENTS.md` files before relevant tool calls. The host remains
responsible for deciding which directory is authorized; the loop does not
change process working directory.

## Model and tool interfaces

The standard `*client.Client` and `*tools.Runtime` satisfy the public
interfaces. Custom adapters implement:

```go
type ChatClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

type ToolRuntime interface {
	Tools() []client.Tool
	Environment() tools.HostEnvironment
	Call(context.Context, client.ToolCall) (client.ToolResult, error)
}
```

When `Request.Stream` is true, a client that also implements
`ChatStream(context.Context, client.ChatRequest) (client.Stream, error)` is
used for streaming. Other clients automatically fall back to `Chat`.

The runtime catalog is both advertisement and authorization. Advertise only
tools the host will execute. Do not advertise `task_start`, `ask_to_user`, or
`task_complete`; the loop owns and intercepts those names. An error returned by
`Call`, or a `ToolResult` with `IsError`, becomes a model-visible tool error so
the model can recover. Use context cancellation for a terminal stop.

## Use builtin and external MCP tools

`tools.Runtime` connects Q's builtin server in-process with the official Go MCP
SDK. It can also own configured stdio and Streamable HTTP MCP sessions:

```go
statuses := toolRuntime.ConfigureExternal(ctx, root, mcpSettings)
for _, status := range statuses {
	if status.Error != "" {
		return fmt.Errorf("connect MCP server %s: %s", status.ID, status.Error)
	}
}

roleTools := agentloop.ScopeTools(toolRuntime, mcpconfig.RoleDefault)
messages := agentloop.PrepareWorkspaceMessages(nil, agentloop.WorkspaceMessageOptions{
	Root: root,
	Tools: roleTools,
})
```

Pass the same scoped runtime to `Request.Tools`. `ScopeTools` uses a
runtime's `ToolsForRole` catalog when available and rejects calls to tools not
advertised for that role. The parent runtime owns every MCP session and must
outlive all scoped views.

`ConfigureExternal` reports failures per server. A failed server does not
disable builtin tools or other successfully connected servers, so the host
must decide whether a partial connection is acceptable.

## Consume events and persist state

Events are ordered and apply to the context used inside the loop:

| Accessor | Host action |
|---|---|
| `Status` | Update transient UI status. |
| `ToolCall` | Record or display the call before execution. |
| `Message` | Append intermediate assistant/tool messages in order. Use `MessageIsToolError` for presentation or audit status. |
| `Question` | Send exactly one `AgentAnswer`, or `ErrInteractionUnavailable` for a headless host. |
| `StreamDelta` | Render partial thinking/response text; do not also treat it as durable final content. |
| `ContextReplacement` | Replace the indexed message in the retained model context. |
| `Compaction` | Apply the plan and summary to the retained model context. |
| `TaskStarted` | Persist the active task and supply it as `ActiveTask` on a later turn. |
| `TaskCompleted` | Clear the persisted active task. |
| `Result` | Append the final assistant message and retain the returned conversation ID. |
| `Err` | Abort the host turn and report the terminal failure. |

A final assistant message is carried by `Result`, not necessarily by a
`Message` event. A durable consumer therefore needs both. Streaming deltas are
for live presentation; persist the complete assistant message from the result.

Keep the user-visible transcript separate from the compact model context. A
simple host can maintain the latter with `memory.Manager`:

```go
manager := memory.New(policy, preparedMessages)

// In the event loop:
if replacement, ok := event.ContextReplacement(); ok {
	if err := manager.Replace(replacement.Index, replacement.Message); err != nil {
		return err
	}
}
if compaction, ok := event.Compaction(); ok {
	if _, err := manager.ApplyCheckpoint(compaction.Plan, compaction.Summary); err != nil {
		return err
	}
}
if message, ok := event.Message(); ok {
	manager.Append(message)
}
if result, ok := event.Result(); ok && result.Response != nil && len(result.Response.Choices) > 0 {
	manager.Append(result.Response.Choices[0].Message)
}
```

Initialize the manager with the exact messages passed to the loop and mirror
events in order. A zero `memory.Policy.ContextWindow` disables compaction.

Before appending an assistant message or tool result, the loop checks the
projected context size against the configured trigger (85% by default). If the
append would cross it, the loop reserves space for that message, compacts the
existing history, and emits the compaction event before the message event.
Pending tool calls survive the checkpoint so their incoming results remain
paired and exact. A message that cannot fit below the trigger, or a checkpoint
that fails to leave the reserved space, ends the turn with an error instead of
committing oversized history. For user input managed outside the loop, use
`memory.Manager.ShouldCompactAfterAppend` and `PlanBeforeAppend` before
appending it, then apply the checkpoint successfully before inserting the input.
`workspace.Session` can persist the full `Transcript`, compact `Context`, and
`ActiveTask`, but `RunAgentLoop` never saves it automatically.

Carry `Result.Response.ConversationID` into the next request when the provider
supports stateful conversations. After compaction, the loop clears its local
conversation ID because the request context has changed; the host should use
the ID from the terminal response rather than retaining an older value.

## Verification checklist

- Exercise the integration from an external test package or downstream module
  so private Q symbols cannot leak into it.
- Cover a direct response, one ordinary tool round, a question, cancellation,
  a tool error, and compaction when those paths are supported by the host.
- Assert that the initial message slice is not changed by
  `PrepareWorkspaceMessages` and that it is not applied twice.
- Verify role scoping with both advertised and rejected MCP calls.
- When enabling Agent Skills without an archive, test a `tools.SkillStore`
  implementation from an external package and verify automatic hint search.
- Run `gofmt`, focused tests, `go test ./...`, and `go vet ./...`.
- When changing Q's public API, also run `go run ./scripts/modulecheck`.

The public declarations and single loop body live in
[`agentloop`](../agentloop). [`agentloop/agentloop_test.go`](../agentloop/agentloop_test.go)
exercises the API from an external package. [`app/agent_public.go`](../app/agent_public.go)
and [`app/agent_loop_facade.go`](../app/agent_loop_facade.go) preserve the old
entry point. The initial API record is in the
[public API plan](embedded-agent-loop-public-api-plan.md).
