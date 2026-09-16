---
title: ACP and external MCP
description: Run q as an ACP agent and attach external MCP tool servers to selected model roles.
sectionLabel: Guide
toc:
  - id: run-q-over-acp
    label: Run q over ACP
  - id: control-plan-automation
    label: Control plan automation
  - id: interaction-and-context
    label: Interaction and context
  - id: attach-external-mcp-servers
    label: Attach MCP servers
  - id: transport-boundaries
    label: Transport boundaries
---

## Run q over ACP

Start q as an Agent Client Protocol server over stdin/stdout:

```powershell
q acp --root C:\work\project
```

ACP mode uses the same durable workspace sessions, root-scoped tools, planning workflow, subagents, learning, and commit workflow as the terminal UI. It advertises commands including `/plan`, `/commit`, `/subagents`, `/subagent`, `/learn`, `/clear`, and `/help` to the connected client.

`--root` defaults to the current directory. It defines the workspace boundary used for files, sessions, instructions, skills, and workspace configuration.

## Control plan automation

Process-only flags can automate plan clarification and proposal approval without changing the persisted settings:

```powershell
q acp --root C:\work\project --auto-resolve --auto-approve
q acp --root C:\work\project --autonomous
```

`--autonomous` enables both behaviors. An explicitly supplied individual flag takes precedence, including forms such as `--auto-approve=false`. These flags affect `/plan`; they do not bypass proposal validation, task execution, review, or commit confirmation.

The slash forms `/auto-resolve`, `/auto-approve`, and `/autonomous` support `on`, `off`, and `status`. Unlike the process flags, `on` and `off` persist to `~/.q/config.yaml`.

## Interaction and context

When the client supports form elicitation, q uses it for plan and commit choices. Otherwise it presents numbered approval actions. Planning and agent questions can consume the next client message as a free-form answer.

q advertises ACP embedded-context support. It preserves resource URI, MIME type, annotations, and content when a session is replayed. When q's TUI is connected to another ACP agent, `@relative/path` and `@"path with spaces"` attach an in-workspace file. q embeds the file if the remote agent supports embedded context and otherwise sends a resource link.

## Attach external MCP servers

Open `/mcp` or run `q mcp` to configure external MCP servers. q supports local stdio processes and Streamable HTTP endpoints. A server can be assigned only to the roles that need its tools rather than being exposed to every model request.

Imported tool names are namespaced to prevent collisions with q's built-in tools and with other servers. Tool results pass through the same Loom capture boundary as built-in results, so oversized payloads become bounded artifact references.

For credentials, configuration maps child environment variables or HTTP headers to source environment-variable names. It does not require writing the secret value into `~/.q/mcp.json`.

## Transport boundaries

MCP servers supplied by an ACP client are scoped to that ACP session and do not become global q configuration. Stdio and Streamable HTTP are supported; SSE transport is not.

An MCP tool receives the authority of its own process or remote service. q can bound the tool result and control which model roles see it, but it cannot turn an arbitrary external server into an operating-system sandbox.
