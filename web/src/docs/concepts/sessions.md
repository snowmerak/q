---
title: Sessions and memory
description: Understand what q persists, compacts, learns, and restores across workspace runs.
sectionLabel: Concepts
toc:
  - id: durable-sessions
    label: Durable sessions
  - id: transcript-and-context
    label: Transcript and context
  - id: delegated-recovery
    label: Delegated recovery
  - id: workspace-memory
    label: Workspace Memory
  - id: learning
    label: Learning
---

## Durable sessions

Each workspace can hold multiple UUID-based sessions under `.q/sessions/`. The startup picker shows their titles and recent activity. Press `d` on an inactive session and `y` to delete it; `n` or Esc cancels. The current session and sessions open in another process cannot be deleted. Deletion removes the conversation and resumable plan checkpoint, but preserves durable Workspace Memory archive records.

One process owns a selected session. Another q process may open a different session in the same workspace, but session locks do not serialize edits to the repository itself.

## Transcript and context

q persists the visible transcript separately from the compacted request context sent to models. When context metadata is available, q can shorten model context while leaving the complete user-visible transcript intact.

The primary session record is:

```text
.q/sessions/<uuid>/session.json
```

Session v2 stores messages in a common format for Chat Completions and Responses. Existing v1 Chat Completions sessions are converted on load and saved in the new format on the next write. The record also keeps the selected chat loop mode and Responses replay state when applicable.

Approved plan execution adds a resumable `plan-execution.json` beside it.

## Delegated recovery

In delegation mode, each child call has a bookmark in `delegations.json` and its own session under `delegates/<invocation-id>/`. Children may have their own nested delegation tree. After a restart, q restores the deepest child first and returns its stored result to the parent call before continuing the parent turn.

If q stopped while a general tool call had no recorded result, recovery reports that call as `unknown` to its agent and does not run it again automatically. An interrupted external ACP child also returns `unknown`; q cannot resume the remote agent's internal turn. `/plan` retains its separate execution checkpoint.

## Workspace Memory

Workspace Memory stores durable messages, tool activity, failures, lifecycle events, and search indexes. It supports retrieval without making one chat projection carry every historical event.

Large tool results are stored as immutable Loom artifacts. The prompt receives a bounded receipt and may inspect only the relevant slice later.

## Learning

After successful turns, Thinker can extract reusable propositions: established workflows, durable project facts, reusable resolutions, and evidence-backed findings likely to affect future work.

Librarian decides whether a proposition should be created, merged, or discarded in the global Library. `/learn off` stops collection and queue processing; it does not delete already queued data.

Run-specific build, test, formatting, and audit snapshots stay in task history instead of becoming durable project facts.
