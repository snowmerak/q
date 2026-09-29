---
title: Subagents
description: Run bounded built-in, custom, and external agents without inheriting hidden conversation state.
sectionLabel: Guide
toc:
  - id: inspect-available-agents
    label: Inspect available agents
  - id: run-one-request
    label: Run one request
  - id: delegate-from-chat
    label: Delegate from chat
  - id: define-an-inner-agent
    label: Define an inner agent
  - id: external-agents
    label: External agents
---

## Inspect available agents

Open **Settings → Subagents** in Studio to inspect built-in definitions and manage custom profiles. The list shows each profile's execution kind, model role, scope, tools, and delegation grants.

The public built-in IDs are:

- `builtin/interviewer`
- `builtin/manager`
- `builtin/senior-developer`
- `builtin/junior-developer`
- `builtin/research`
- `builtin/web-search`
- `builtin/web-tester`

## Run one request

Pass the complete task context in the request. A child does not automatically inherit the parent conversation.

```text
/subagent builtin/senior-developer review the cancellation path in app/model.go
```

Explicit `/subagent` calls in the bare q compatibility client use a custom profile's short name. Delegation grants stored inside profiles use canonical IDs such as `builtin/senior-developer`, `global/code-reader`, or `workspace/browser-check`.

## Delegate from chat

Ordinary chat exposes both direct workspace tools and delegation. The main agent can work directly or coordinate bounded subagents based on the request. The manager owns requirements and planning; the senior developer can edit directly or assign implementation to the junior developer, then review the result. Use `/subagent <name> <request>` when you want to select a role explicitly.

Studio shows each child below its parent in the session tree. Select a child to open its progress, transcript, and tool calls. Each call saves a child session and a bookmark under the parent. On restart, q recovers nested children before continuing the parent. A tool call with no recorded result returns `unknown` and is not run again automatically. An interrupted external ACP invocation also returns `unknown` because its internal turn cannot be resumed.

In a persisted session backed by a clean Git branch, a mutating inner child works on a local `q/delegate/<invocation-id>` branch in a linked worktree. When it succeeds, q commits remaining changes and returns an internal change request pinned to base and head commits. The caller reads that diff, then merges or closes the request. Nested children use the same flow, so a senior developer can review and merge a junior developer's branch before returning its own change request. Studio shows the request status and branch in the session tree. This local flow does not require a remote push.

Every inner task result has a concise `summary` and may include a `report` containing up to 256 KiB of final Markdown analysis, design rationale, review notes, or research synthesis. Large results are stored in Loom; the caller receives a reference and bounded preview and can retrieve the complete report when needed.

## Define an inner agent

Inner profiles select a q model role, an explicit tool list, and directly callable delegates.

```yaml
version: 1
name: code-reader
description: Explain the requested code.
kind: inner
role: advisor
system_prompt: |
  Read the requested code and explain its behavior with concrete file references.
tools:
  - list_directory
  - read_file
delegates: []
```

Profiles live in `~/.q/subagents/` or `<workspace>/.q/subagents/`. A workspace profile replaces the complete global profile with the same name.


## External agents

External profiles bind to an enabled ACP connection. They store a system prompt and whether the remote agent may mutate the workspace; they do not select q tools, delegates, or a q model role.

Because ACP session creation has no system-message field, q prepends the stored system prompt to the first ordinary ACP prompt. Missing or disabled connections make the profile unavailable without deleting it.
