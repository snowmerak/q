---
name: q-agentloop-embedding
description: Embed or extend q's exported Go agent loop with model clients, tool runtimes, MCP role scoping, hooks, compaction, and caller-owned sessions. Use for implementations or reviews involving github.com/snowmerak/q/agentloop, not for ordinary q CLI operation.
---

# Q agent-loop embedding

Use `agentloop.Run` as the execution boundary. Do not copy q's private app loop
or make an external integration depend on the `app` package.

Before changing an integration, read
[`docs/agent-loop-embedding.md`](../../../docs/agent-loop-embedding.md). Inspect
[`agentloop/types.go`](../../../agentloop/types.go) when exact public fields are
needed, and [`agentloop/loop_external_test.go`](../../../agentloop/loop_external_test.go)
for downstream-style fakes and assertions.

## Preserve these contracts

- Keep `ModelClient` and `ToolRuntime` adapters narrow. The creator owns their
  configuration, concurrency safety, and closure.
- Treat `Run` as one synchronous turn controlled by the caller's context.
- Keep the durable transcript separate from `Result.Context`, which is the
  compacted context for a later model request.
- Deliver or persist `Event` values in order. An event callback error aborts
  the run.
- Carry `ConversationID` and active task state across turns when the host
  persists sessions.
- Set an explicit context policy when compaction is desired; a zero context
  window disables it.
- Use a positive `MaxRounds` at externally reachable service boundaries unless
  the caller explicitly wants q's unbounded behavior.

## Choose the tool boundary

Use `tools.Runtime` for q's builtin Go-MCP runtime. After configuring external
stdio or Streamable HTTP MCP servers, pass `runtime.ForRole(role)` so advertised
tools and dispatch authorization use the same role catalog. Do not register
`task_start`, `ask_to_user`, or `task_complete`; the loop owns those names.

For another execution system, implement `Tools` and `Call` directly. Advertise
only operations the host is prepared to authorize and execute. Both an error
result and an error returned by `Call` become a model-visible tool error. Use
context cancellation or an event callback error when the whole run must stop.

## Add host behavior deliberately

Use `Hooks.Event` for ordered transcript, UI, task, and compaction integration.
Use `Hooks.Ask` only when the host can actually pause for input. A headless host
may omit it or return `agentloop.ErrInteractionUnavailable` so the model can
continue or finish as blocked.

Supply `Request.SkillHints` only when a host-side skill search adapter exists.
It is optional and distinct from model-callable `search_skills` and `get_skill`
tools.

## Verify the integration

Add a test in an external test package or a small downstream module so private
q symbols cannot leak into the design. Cover the paths changed by the task,
especially tool dispatch, context compaction, question handling, cancellation,
and task completion. Run `gofmt`, focused tests, `go test ./...`, and
`go vet ./...`; run q's module distribution check when changing the public
package or its dependencies.
