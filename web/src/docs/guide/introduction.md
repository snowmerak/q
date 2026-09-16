---
title: Introduction
description: Understand what q owns, where it runs, and which workflow to use first.
sectionLabel: Guide
toc:
  - id: what-q-is
    label: What q is
  - id: choose-a-workflow
    label: Choose a workflow
  - id: what-q-keeps
    label: What q keeps
  - id: next-step
    label: Next step
---

## What q is

q is a workspace-native coding agent for the terminal. Start it inside a repository or project directory and it combines conversation, file tools, shell commands, model providers, planning, subagents, review, and durable history in one Go binary.

The workspace is the operating boundary for q's file tools. Shell commands start there too, but they are **not** an operating-system sandbox. Review commands and approvals with the same care you would use for a local development shell.

## Choose a workflow

Use ordinary chat when the request is focused and you want q to inspect or edit directly.

Use `/plan` when the work needs clarification, an explicit proposal, approval before execution, or a resumable checkpoint. q moves through Griller, Scout, Planner, execution, and Planner review.

Use `q sprint` for the same planned workflow without the interactive approval steps:

```powershell
q sprint implement the requested feature
```

Use `/subagent` when a bounded specialist should handle one request. Use `q remote` when another trusted process needs the same session and agent runtime through HTTP.

## What q keeps

Each workspace can contain multiple durable sessions. The visible transcript, compacted model context, task lifecycle, plan checkpoints, and searchable workspace history are stored separately so long-running work can remain inspectable without sending the entire past on every turn.

q also supports Agent Skills. Skill metadata is searched on demand; a full `SKILL.md` enters model context only after the agent loads an applicable skill.

## Next step

Install q with `go install` or from a source checkout, then start it in the project you want it to understand.
