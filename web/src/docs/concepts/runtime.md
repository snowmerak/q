---
title: Runtime model
description: See how the TUI, providers, tools, memory, and supporting services divide ownership.
sectionLabel: Concepts
toc:
  - id: one-interactive-process
    label: One interactive process
  - id: workspace-runtime
    label: Workspace runtime
  - id: durable-services
    label: Durable services
  - id: why-the-boundaries-matter
    label: Why the boundaries matter
---

## One interactive process

An ordinary q process owns one selected workspace session and coordinates the terminal UI, current model turn, active agents, and interaction lifecycle.

It also supervises a managed Gateway child on a private loopback endpoint. Provider configuration can be replaced without tying the provider process lifetime to stored workspace state.

## Workspace runtime

Workspace file tools are rooted at the directory where q starts. Reads and edits use path checks and stale-content anchors. Shell commands start in the workspace, but they are not an operating-system sandbox.

Optional LSP sessions are also rooted and read-only from the model's perspective. q exposes diagnostics, hover, definitions, references, and symbols while rejecting server-originated edits.

## Durable services

q separates durable ownership by concern:

| Component | Responsibility |
| --- | --- |
| Workspace Memory | Task history, records, and workspace search indexes |
| Global Library | Shared skills, propositions, and judging queue |
| Loom | Immutable capture for large tool results |
| Usage service | Token events, rollups, archives, and dashboard data |

Ordinary q processes ensure these services are available when needed. Standalone commands can keep them alive independently of a TUI.

## Why the boundaries matter

Several q processes can share durable services without sharing a selected chat session or live provider conversation. A process lock owns one session, while another session in the same workspace may run concurrently.

The boundary also keeps derived indexes replaceable. JSON records remain the source of truth; Bleve and HNSW data can be rebuilt.
