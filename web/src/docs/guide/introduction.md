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

q is a workspace-native coding environment. Its local Studio combines conversation, file tools, shell commands, model providers, occupational subagents, repository review, and durable history in one Go binary.

The workspace is the operating boundary for q's file tools. Shell commands start there too, but they are **not** an operating-system sandbox. Review commands and approvals with the same care you would use for a local development shell.

## Choose a workflow

Create or register a repository session in Studio when you want q to inspect, edit, or coordinate work in that workspace.

Ordinary chat can coordinate occupational subagents while retaining direct workspace tools. The manager handles requirements and planning; the senior developer can implement directly or assign and review implementation.

Ask the main agent to delegate when a specialist should handle a bounded request. Bare `q` remains available as a compatibility chat client with `/subagent` for explicit role selection.

## What q keeps

Each workspace can contain multiple durable sessions, while Studio presents registered root sessions from different repositories together. The visible transcript, compacted model context, task lifecycle, delegation state, and searchable workspace history are stored separately so long-running work can remain inspectable without sending the entire past on every turn.

q also supports Agent Skills. Skill metadata is searched on demand; a full `SKILL.md` enters model context only after the agent loads an applicable skill.

## Next step

Install q with `go install` or from a source checkout, run `q studio`, then register the repository you want it to understand.
