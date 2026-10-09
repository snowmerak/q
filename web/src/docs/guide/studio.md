---
title: Q Studio
description: Run sessions, inspect delegation trees, review changes, and configure q from the local web interface.
sectionLabel: Guide
toc:
  - id: start-studio
    label: Start Studio
  - id: sessions-and-delegation
    label: Sessions and delegation
  - id: review-repository-work
    label: Review repository work
  - id: settings-and-operations
    label: Settings and operations
  - id: local-runtime-boundary
    label: Local runtime boundary
---

## Start Studio

Q Studio is the primary interface for daily work. It is an embedded Svelte application served by the q binary, so installation does not require a separate web service.

```powershell
q studio
```

Studio listens on `127.0.0.1` with a random available port and opens a browser by default. Studio also starts the separate `q gateway start` server using its saved listener settings. It prints the Gateway URL in the terminal. Before providers are configured, the Gateway starts when the first provider is saved. The Gateway stops with Studio; run `q gateway start` separately when it should stay up independently.

Use a fixed local URL with:

```powershell
q studio --host 127.0.0.1 --port 7070
```

To connect from another device on a trusted network, listen on every IPv4 interface and open `http://<this-machine-ip>:7070` from that device:

```powershell
q studio --host 0.0.0.0 --port 7070 --no-open
```

`--host` accepts an IP address such as `127.0.0.1`, `0.0.0.0`, `::1`, or `::`. Port `0` selects a random available port, and `--no-open` disables automatic browser launch.

**Studio has no built-in HTTP authentication.** A Studio client can trigger file, process, Git, and configuration operations with the q process's permissions. Use a non-loopback host only on a trusted, firewalled network or behind an authenticated reverse proxy.

The directory where Studio starts does not limit its workspaces. Each registered root session records the repository directory where its agent runs.

## Sessions and delegation

Open **Sessions**, choose a repository directory, then register an existing root session or create a new one. Root sessions from different repositories appear in one list. Selecting a session opens its chat, context usage, current run controls, and delegated children.

Messages use safe Markdown rendering with syntax highlighting. Runs continue independently of the browser connection, and Studio reconnects to their event stream after a refresh. When an agent asks a question, pauses, or needs guidance, respond from the selected run. You can also pause, resume, or stop active work.

Delegations are shown as a tree. Select a child to inspect its own transcript and nested work. A completed child can be deleted from its chat header. Deletion removes that child and all descendants while retaining the completed tool exchange in the parent transcript.

## Review repository work

Open **Changes** for a repository-wide view of staged, unstaged, renamed, and untracked files. Select a file for a bounded, highlighted diff with anchors for individual lines.

The commit workflow generates one or more Conventional Commit proposals. You can edit messages, regenerate the proposal, choose the commits to create, and optionally push them. Studio checks that the reviewed index still matches before committing.

## Settings and operations

**Settings** owns global models and role assignments, Gateway providers, System One providers and decision models, runtime limits, services, occupational subagents, Agent Skills, MCP, language servers, and `.qignore`. Fields save when they change; there is no separate save shortcut.

Repository model overrides appear when a workspace is selected. Provider changes are applied to subsequent turns without restarting Studio. API key values are handled as secrets and are not returned by read APIs.

**Operations** shows token usage, active and resident runs, local service health, retained data, and bounded logs. **Help** lists Studio shortcuts and maps compatibility commands to their current Studio screens.

## Local runtime boundary

Studio binds to loopback by default and performs file, process, Git, and configuration work through the local q process. The browser never receives direct filesystem access. Shell commands still have the authority of the local q process and are not an operating-system sandbox. `--host` can deliberately widen the network boundary, so access control must then be supplied by the network or an authenticated reverse proxy.

Bare `q` remains available as a compatibility chat client, and `q acp` remains the ACP server entry point. Configuration commands such as `q model`, `q gateway`, `q systemone`, `q subagents`, `q skills`, `q mcp`, and `q lsp` open the corresponding Studio surface.
