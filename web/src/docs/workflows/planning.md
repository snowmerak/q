---
title: Delegated work
description: Coordinate professional subagents for requirements, research, implementation, and review.
sectionLabel: Guide
toc:
  - id: start-a-task
    label: Start a task
  - id: roles-and-models
    label: Roles and models
  - id: former-plan-mode
    label: Former plan mode
---

## Start a task

Describe the outcome you want in a workspace session. The default loop has direct workspace tools and can delegate requirements and planning to the manager, focused investigation to research, and technical work or review to the senior developer. There is no fixed sequence of agents.

For a focused PM request, use `/subagent builtin/manager <request>` in chat.

## Roles and models

The manager is a PM role responsible for requirements, priorities, acceptance criteria, and a work plan when needed. The interviewer identifies decisions that require user input, and research investigates focused questions. Each can read relevant workspace evidence directly.

The senior developer uses the `reviewer` model role and has editing and command tools. It can make changes directly or assign bounded implementation to the junior developer, then inspect actual changes and verification and request corrections. The junior developer uses the `coder` model role and also has editing and command tools. The separate `builtin/reviewer` subagent has been removed; ask `builtin/senior-developer` for technical review.

## Former plan mode

`/plan` and its `/auto-approve`, `/auto-resolve`, and `/autonomous` controls no longer start work. Planning is now a request handled by the manager within ordinary delegation. Existing plan checkpoints remain on disk as legacy data and do not resume as new plan runs.
