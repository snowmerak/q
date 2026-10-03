---
title: Configuration
description: Locate q's personal settings, workspace state, profiles, and rebuildable indexes.
sectionLabel: Reference
toc:
  - id: personal-state
    label: Personal state
  - id: import-and-export
    label: Import and export
  - id: workspace-state
    label: Workspace state
  - id: source-of-truth
    label: Source of truth
---

## Personal state

| Path | Purpose |
| --- | --- |
| `~/.q/config.yaml` | Main model, roles, per-model API modes, context, Loom, and LSP configuration. |
| `~/.q/providers.json` | Managed Gateway providers and model metadata. |
| `~/.q/gateway.json` | Standalone Gateway listener and key metadata. |
| `~/.q/gateway.key` | Private master key used to verify Gateway API keys. |
| `~/.q/systemone.json` | System One providers, model routing, listener, and API key metadata. |
| `~/.q/systemone.key` | Private master key used to verify System One API keys. |
| `~/.q/library.json` | Global Library loopback listener settings. |
| `~/.q/workspace-memory.json` | Workspace Memory loopback listener settings. |
| `~/.q/usage.json` | Token Usage service loopback listener settings. |
| `~/.q/mcp.json` | External MCP profiles and role assignments. |
| `~/.agents/skills/` | Portable global Agent Skills discovered but not managed by q. |
| `~/.q/skills/` | q-managed global Agent Skills. |
| `~/.q/subagents/` | Global custom subagent profiles. |
| `~/.q/studio-sessions.json` | Root sessions registered in Studio and their workspace locations. |
| `~/.q/logs/thinker/` | Short-lived Thinker invocation diagnostics. |
| `~/.q/usage/usage.sqlite` | Recent token events and daily rollups. |
| `~/.q/usage/archive/` | Parquet archives for older raw usage events. |

Use Studio for ordinary configuration. Edit these files directly only when automation requires it.

## Import and export

Open **Settings → Import / Export** in Studio to move global settings. The dialog has Models, Providers, System One, Runtime, Services, Subagents, and Integrations tabs. Selections persist across tabs and export together in one JSON file.

To import, choose a file, select its items, review additions and replacements, then apply. Matching item IDs are replaced; unselected items remain unchanged. Select related model groups, ACP connections, or MCP servers together when they do not already exist at the destination. Missing references are rejected before writing. Listener changes take effect when their services restart.

The file envelope contains `format: "q-settings"`, `version: 1`, `scope: "global"`, and `sections`. Each section maps item IDs to their data. Files are limited to 4 MiB. API keys, provider header/body/environment maps, ACP environment values, and cache passwords are excluded; existing local values are preserved when importing matching connections. Workspace overrides, Skills, and `.qignore` are outside this global settings format.

## Workspace state

| Path | Purpose |
| --- | --- |
| `.q/sessions/<uuid>/session.json` | Transcript, context, title, lifecycle, and learning state. |
| `.q/sessions/<uuid>/delegations.json` | Bookmarks for delegated child calls. |
| `.q/sessions/<uuid>/delegates/<invocation-id>/` | Child session, execution state, and nested delegates. |
| `.q/model.json` | Workspace model-role overrides. |
| `.q/learning.json` | Workspace learning switch. |
| `.q/lsp.json` | Workspace LSP roots and overrides. |
| `.q/data/` and `.q/index/` | Durable records and derived indexes. |
| `.q/loom/` | Content-addressed tool artifacts and GC metadata. |
| `.agents/skills/` | Portable workspace Agent Skills discovered but not managed by q. |
| `.q/skills/` | q-managed workspace Agent Skills. |
| `.q/subagents/` | Workspace custom subagent profiles. |
| `AGENTS.md` | Workspace and nested path instructions. |
| `.qignore` | Discovery exclusions. |

## Source of truth

JSON records are the source of truth. Bleve and HNSW indexes are derived and rebuildable.

Loom garbage collection protects references from active session projections and saved records, subject to its configured grace period. Deleting workspace `.q` state removes durable q history for that workspace; it does not revert repository files.
