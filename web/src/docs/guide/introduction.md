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

q is a workspace-native coding agent for the terminal. Start it inside a repository or project directory and it combines conversation, file tools, shell commands, model providers, subagents, review, and durable history in one Go binary.

The workspace is the operating boundary for q's file tools. Shell commands start there too, but they are **not** an operating-system sandbox. Review commands and approvals with the same care you would use for a local development shell.

## Choose a workflow

Use ordinary chat when the request is focused and you want q to inspect or edit directly.

Ordinary chat can coordinate occupational subagents while retaining direct workspace tools. The manager handles requirements and planning; the senior developer can implement directly or assign and review implementation.

Use `/subagent` when a bounded specialist should handle one request.

## What q keeps

Each workspace can contain multiple durable sessions. The visible transcript, compacted model context, task lifecycle, delegation state, and searchable workspace history are stored separately so long-running work can remain inspectable without sending the entire past on every turn.

q also supports Agent Skills. Skill metadata is searched on demand; a full `SKILL.md` enters model context only after the agent loads an applicable skill.

## Next step

Install q with `go install` or from a source checkout, then start it in the project you want it to understand.
