---
title: Configuration
description: Locate q's personal settings, workspace state, profiles, and rebuildable indexes.
sectionLabel: Reference
toc:
  - id: personal-state
    label: Personal state
  - id: workspace-state
    label: Workspace state
  - id: source-of-truth
    label: Source of truth
---

## Personal state

| Path | Purpose |
| --- | --- |
| `~/.q/config.yaml` | Main model, roles, context, Loom, and LSP configuration. |
| `~/.q/providers.json` | Managed Gateway providers and model metadata. |
| `~/.q/gateway.json` | Standalone Gateway listener and key metadata. |
| `~/.q/remote.json` | Remote listener, authentication switch, and key metadata. |
| `~/.q/remote.key` | Private master key used to verify Remote API keys. |
| `~/.q/mcp.json` | External MCP profiles and role assignments. |
| `~/.q/skills/` | q-managed global Agent Skills. |
| `~/.q/subagents/` | Global custom subagent profiles. |
| `~/.q/usage/usage.sqlite` | Recent token events and daily rollups. |

Use the TUI for ordinary configuration. Edit these files directly only when automation requires it.

## Workspace state

| Path | Purpose |
| --- | --- |
| `.q/sessions/<uuid>/session.json` | Transcript, context, title, lifecycle, and learning state. |
| `.q/sessions/<uuid>/plan-execution.json` | Resumable approved-plan checkpoint. |
| `.q/plan-executions/` | Completed execution snapshots. |
| `.q/model.json` | Workspace model-role overrides. |
| `.q/learning.json` | Workspace learning switch. |
| `.q/lsp.json` | Workspace LSP roots and overrides. |
| `.q/data/` and `.q/index/` | Durable records and derived indexes. |
| `.q/loom/` | Content-addressed tool artifacts and GC metadata. |
| `.q/skills/` | q-managed workspace Agent Skills. |
| `.q/subagents/` | Workspace custom subagent profiles. |
| `AGENTS.md` | Workspace and nested path instructions. |
| `.qignore` | Discovery exclusions. |

## Source of truth

JSON records are the source of truth. Bleve and HNSW indexes are derived and rebuildable.

Loom garbage collection protects references from active session projections and plan checkpoints, subject to its configured grace period. Deleting workspace `.q` state removes durable q history for that workspace; it does not revert repository files.
