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

Studio listens on loopback and opens a browser. Use `--port <port>` to select a port or `--no-open` to leave the browser closed. The directory where Studio starts does not limit its workspaces. Each registered root session records the repository directory where its agent runs.

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

Studio binds to loopback and performs file, process, Git, and configuration work through the local q process. The browser never receives direct filesystem access. Shell commands still have the authority of the local q process and are not an operating-system sandbox.

Bare `q` remains available as a compatibility chat client, and `q acp` remains the ACP server entry point. Configuration commands such as `q model`, `q gateway`, `q systemone`, `q subagents`, `q skills`, `q mcp`, and `q lsp` open the corresponding Studio surface.
