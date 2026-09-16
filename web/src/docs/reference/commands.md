---
title: Commands
description: Find the interactive and standalone commands that control q's main workflows.
sectionLabel: Reference
toc:
  - id: chat-commands
    label: Chat commands
  - id: standalone-commands
    label: Standalone commands
  - id: essential-keys
    label: Essential keys
---

## Chat commands

| Command | Purpose |
| --- | --- |
| `/plan [request]` | Clarify, propose, approve, execute, and review a plan. |
| `/auto-resolve [on\|off\|status]` | Control engineering-default answers to plan clarification. |
| `/auto-approve [on\|off\|status]` | Control automatic approval of valid plan proposals. |
| `/autonomous [on\|off\|status]` | Control both plan automation settings together. |
| `/changes` | Browse staged, unstaged, and untracked changes. |
| `/commit` | Generate and review a commit proposal. |
| `/sessions` | Open another saved workspace session. |
| `/new` | Create and switch to a new session. |
| `/clear` | Clear the current conversation projection and plan checkpoint. |
| `/learn [on\|off\|status]` | Checkpoint or control durable learning for this workspace. |
| `/model` | Assign models and configure fallback groups. |
| `/gateway` | Configure providers and Gateway listener settings. |
| `/library` | Configure the global Library listener. |
| `/loom` | Inspect Loom storage and configure garbage collection. |
| `/ignore` | Edit workspace discovery rules in `.qignore`. |
| `/skills` | Manage global and workspace Agent Skills. |
| `/subagents` | Manage built-in, custom, and external agents. |
| `/subagent <name> <request>` | Run one bounded subagent request. |
| `/mcp` | Configure external MCP servers. |
| `/lsp` | Configure language-server profiles and roots. |
| `/help` | Open the complete command and key guide. |

## Standalone commands

| Command | Purpose |
| --- | --- |
| `q sprint <request...>` | Run one autonomous planned workflow. |
| `q remote` | Start the foreground Remote REST host. |
| `q remote config` | Configure Remote listener and API keys. |
| `q gateway` | Configure the standalone Gateway. |
| `q gateway start` | Start the OpenAI-compatible Gateway. |
| `q library` | Configure the global Library listener. |
| `q library start` | Keep the global Library running as a foreground service. |
| `q memory` | Keep Workspace Memory running independently. |
| `q usage` | Open the local token-usage dashboard. |
| `q commit` | Open the standalone commit workflow. |
| `q model` | Configure model and role assignments. |
| `q subagents` | Manage subagent profiles and ACP bindings. |
| `q skills` | Manage Agent Skills. |
| `q mcp` | Configure external MCP servers. |
| `q lsp` | Configure language servers and workspace roots. |
| `q ignore` | Edit `.qignore`. |
| `q help` | Open the command and key guide without starting chat services. |
| `q acp [flags]` | Run q as an ACP server over stdin/stdout. |

`q agents` is a compatibility alias for `q subagents`.

## Essential keys

| Key | Action |
| --- | --- |
| `Enter` / `Ctrl+S` | Send the current message. |
| `Shift+Enter` | Insert a newline. |
| `Ctrl+O` | Collapse or expand tool result bodies. |
| `Ctrl+G` | Collapse or expand the subagent trace. |
| `Ctrl+H` | Open or close help. |
| `Ctrl+C` | Interrupt an active turn; quit while idle. |
| `Esc` | Leave the current screen or quit chat. |
