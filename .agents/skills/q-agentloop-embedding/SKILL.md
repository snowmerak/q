---
name: q-agentloop-embedding
description: Embed or extend Q's existing public Go Agent Loop in another application, including workspace setup, events, MCP tool scoping, compaction, and caller-owned sessions. Use for github.com/snowmerak/q/app integrations, not ordinary Q CLI operation.
---

# Q Agent Loop embedding

Use `app.RunAgentLoop` as the only execution boundary. It is the loop used by
Q's TUI and ACP host. Do not copy it into an `agent`/`agentloop` package, add a
parallel state machine, or reimplement its orchestration and compaction.

Before implementing an integration, read
[`docs/agent-loop-embedding.md`](../../../docs/agent-loop-embedding.md). Consult
[`app/agent_public.go`](../../../app/agent_public.go) for the exact current
contract and [`app/agent_public_test.go`](../../../app/agent_public_test.go) for
an external-package tool round.

## Build the minimum host

- Create or adapt an `app.ChatClient`; the standard `*client.Client` already
  satisfies it.
- Create a workspace-rooted `app.AgentToolRuntime`; normally use
  `tools.NewRuntime(ctx, root)`.
- If the embedded host needs Agent Skills without the Workspace Archive or Q
  Library, implement `tools.SkillStore` (`Search`, `Save`, and `Delete`) and use
  `tools.NewRuntimeWithSkillStore(ctx, root, store)`. The plain constructor does
  not advertise `search_skills` or `get_skill` because it has no Skill index.
- Call `app.PrepareWorkspaceMessages` once when initializing the session, then
  append user turns and ordered events. Never apply it repeatedly to retained
  history.
- Set `WorkingDirectory` to the same authorized root so nested `AGENTS.md`
  instructions load for relevant tool calls.
- Start `RunAgentLoop` in a goroutine and drain its non-nil event channel until
  the loop closes it. The host must not close the channel.
- Give externally reachable runs an appropriate context deadline. The public
  loop intentionally has no separate round-limit option.

`RunAgentLoop` requires a client and tool runtime and owns neither lifetime.
For a plain model completion without tools, use the model client directly.
The runtime also does not close an injected Skill store; the host owns it.

With `NewRuntimeWithSkillStore`, keep Skill discovery and hint orchestration in
Q. The runtime reconciles metadata into the store, `get_skill` reads bodies
from the existing registry, and the loop uses the same store for automatic
hints on user input, `task_start`, and `ask_to_user` answers. Do not add a
second Skill scanner or hint injector in the embedding host. A custom store may
optionally implement `Prepare` to add embeddings; it does not need `Get`.

## Preserve event and session semantics

Process events in order. Append `Message` values to model context, apply
`ContextReplacement` at its index, apply `Compaction` through
`memory.Manager.ApplyCheckpoint`, and append the final assistant message from
`Result`. Streaming deltas are presentation data, not the durable final
message.

Persist `TaskStarted` and pass it back as `AgentLoopRequest.ActiveTask`; clear
it after `TaskCompleted`. Carry the terminal response's `ConversationID` into
the next turn. Keep the full audit transcript separate from compact model
context; `workspace.Session` can store both, but the loop never saves it.

For `Question`, send exactly one `AgentAnswer`. A headless host should send
`AgentAnswer{Err: app.ErrInteractionUnavailable}` so the model receives a
recoverable tool error. Other answer errors terminate the loop.

If the channel closes without `Result` or `Err`, check `ctx.Err()`; cancellation
can stop emission before a terminal event is delivered.

## Keep tool authorization aligned

Use the same runtime for message preparation and loop execution. For Q's
runtime, configure external MCP sessions on the parent and pass
`app.ScopeTools(runtime, role)` to both places. The scoped view advertises only
the role catalog and rejects hidden calls; the parent runtime owns all MCP
sessions.

A custom runtime's `Tools` list is an authorization boundary. Advertise only
callable operations and do not define `task_start`, `ask_to_user`, or
`task_complete`, which the loop owns. Model-visible tool failures belong in a
returned error or `ToolResult.IsError`; cancel the context for a terminal stop.

## Change the public loop safely

When the task changes Q itself, modify the existing implementation in place.
Keep TUI, ACP, internal continuations, and external users on `RunAgentLoop`.
Before finishing, confirm that only one model/tool round loop exists and no
bridge contains copied orchestration logic.

Test integrations from `package app_test` or a downstream module. Cover the
changed paths rather than wording: tool dispatch, questions, ordered context
updates, compaction, cancellation, and role rejection as applicable. Run
`gofmt`, focused tests, `go test ./...`, and `go vet ./...`; run
`go run ./scripts/modulecheck` after public API or dependency changes.
