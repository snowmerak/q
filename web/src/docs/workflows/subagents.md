---
title: Subagents
description: Run bounded built-in, custom, and external agents without inheriting hidden conversation state.
sectionLabel: Guide
toc:
  - id: inspect-available-agents
    label: Inspect available agents
  - id: run-one-request
    label: Run one request
  - id: define-an-inner-agent
    label: Define an inner agent
  - id: external-agents
    label: External agents
---

## Inspect available agents

Open `/subagents` to inspect built-in definitions and manage custom profiles. The list shows each profile's execution kind, model role, scope, tools, and delegation grants.

The public built-in IDs are:

- `builtin/scout`
- `builtin/griller`
- `builtin/planner`
- `builtin/executor`
- `builtin/reviewer`
- `builtin/coder`
- `builtin/web-search`
- `builtin/web-tester`

## Run one request

Pass the complete task context in the request. A child does not automatically inherit the parent conversation.

```text
/subagent builtin/scout explain the cancellation path in app/model.go
```

Custom profiles use their bare name in the TUI. Delegation grants stored inside profiles use canonical IDs such as `builtin/scout`, `global/code-reader`, or `workspace/browser-check`.

## Define an inner agent

Inner profiles select a q model role, an explicit tool list, and directly callable delegates.

```yaml
version: 1
name: code-reader
description: Explain the requested code.
kind: inner
role: scout
system_prompt: |
  Read the requested code and explain its behavior with concrete file references.
tools:
  - list_directory
  - read_file
delegates:
  - builtin/scout
```

Profiles live in `~/.q/subagents/` or `<workspace>/.q/subagents/`. A workspace profile replaces the complete global profile with the same name.

## External agents

External profiles bind to an enabled ACP connection. They store a system prompt and whether the remote agent may mutate the workspace; they do not select q tools, delegates, or a q model role.

Because ACP session creation has no system-message field, q prepends the stored system prompt to the first ordinary ACP prompt. Missing or disabled connections make the profile unavailable without deleting it.
