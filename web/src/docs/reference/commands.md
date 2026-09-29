---
title: Commands
description: Find the chat and CLI commands that control q's main workflows.
sectionLabel: Reference
toc:
  - id: chat-commands
    label: Chat commands
  - id: cli-commands
    label: CLI commands
  - id: essential-keys
    label: Essential keys
---

## Chat commands

| Command | Purpose |
| --- | --- |
| `/changes` | Browse staged, unstaged, and untracked changes. |
| `/commit` | Generate and review a commit proposal. |
| `/sessions` | Open another saved workspace session. |
| `/new` | Create and switch to a new session. |
| `/clear` | Clear the current conversation projection. |
| `/learn [on\|off\|status]` | Checkpoint or control durable learning for this workspace. |
| `/model` | Assign models and configure fallback groups. |
| `/gateway` | Configure providers and Gateway listener settings. |
| `/systemone` | Configure System One providers, decision models, keys, and listener settings. |
| `/library` | Configure the global Library listener. |
| `/loom` | Inspect Loom storage and configure garbage collection. |
| `/ignore` | Edit workspace discovery rules in `.qignore`. |
| `/skills` | Manage global and workspace Agent Skills. |
| `/subagents` | Manage built-in, custom, and external agents. |
| `/subagent <name> <request>` | Run one bounded subagent request. |
| `/mcp` | Configure external MCP servers. |
| `/lsp` | Configure language-server profiles and roots. |
| `/help` | Open the complete command and key guide. |


## CLI commands

| Command | Purpose |
| --- | --- |
| `q studio [--port <port>] [--no-open]` | Start the embedded Studio web interface on loopback. |
| `q gateway` | Open Gateway providers and listener settings in Studio. |
| `q gateway start` | Start the OpenAI-compatible Gateway. |
| `q systemone` | Open System One providers, decision models, and keys in Studio. |
| `q systemone start [--host <ip>] [--port <port>]` | Start the System One decision API. |
| `q library` | Open global Library listener settings in Studio. |
| `q library start` | Keep the global Library running as a foreground service. |
| `q memory` | Keep Workspace Memory running independently. |
| `q usage` | Open Operations in Studio. |
| `q commit` | Open the current repository in Studio Changes and commit review. |
| `q model` | Open global and workspace model assignments in Studio. |
| `q subagents` | Open subagent profiles and ACP bindings in Studio. |
| `q skills` | Open Agent Skills in Studio. |
| `q mcp` | Open MCP configuration in Studio. |
| `q lsp` | Open language servers and workspace roots in Studio. |
| `q ignore` | Open the current repository's `.qignore` editor in Studio. |
| `q help` | Open Studio Help. |
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
