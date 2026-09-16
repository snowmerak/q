---
title: Plan and execute
description: Use q's approval-gated workflow for work that needs clarification, execution, and review.
sectionLabel: Guide
toc:
  - id: start-a-plan
    label: Start a plan
  - id: workflow-roles
    label: Workflow roles
  - id: automation-controls
    label: Automation controls
  - id: resume-after-interruption
    label: Resume after interruption
---

## Start a plan

Use `/plan` when the work should be clarified, explicitly approved, divided into reviewed tasks, or recoverable after interruption.

```text
/plan replace the current cache with a bounded LRU implementation
```

Ordinary chat requests can still use tools directly. They do not enter the planning workflow automatically.

For a non-interactive invocation, run the same workflow with per-process automation enabled:

```powershell
q sprint replace the current cache with a bounded LRU implementation
```

## Workflow roles

The default path is:

1. **Griller** asks only for decisions repository evidence cannot answer.
2. **Scout** performs bounded, non-mutating investigation when more evidence is required.
3. **Planner** proposes tasks, targets, executors, completion criteria, and verification.
4. **Executor** runs the approved task through Coder or a configured external executor.
5. **Planner review** accepts the result or sends bounded retry feedback.

Tasks run sequentially and share a bounded attempt count. Files already changed are not automatically reverted when execution stops.

## Automation controls

`/auto-resolve` controls whether q answers plan clarification with its engineering defaults. `/auto-approve` controls whether a valid Planner proposal is approved automatically. `/autonomous` changes both switches together.

```text
/auto-resolve on
/auto-approve on
/autonomous status
```

Automation does not bypass proposal validation, task execution, or Planner review.

## Resume after interruption

Active execution is checkpointed under the selected session:

```text
.q/sessions/<uuid>/plan-execution.json
```

After a restart, q offers Resume, Inspect, and Discard. Discard removes the checkpoint; it does not undo files that an executor already changed.
