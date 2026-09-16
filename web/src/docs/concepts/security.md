---
title: Security boundaries
description: Know which q boundaries constrain tools and which ones require external isolation.
sectionLabel: Concepts
toc:
  - id: workspace-file-boundary
    label: Workspace file boundary
  - id: shell-execution
    label: Shell execution
  - id: credentials
    label: Credentials
  - id: network-services
    label: Network services
---

## Workspace file boundary

q's built-in file operations are jailed to the active workspace. Anchored edits reject stale content, and structured paths can activate nested `AGENTS.md` instructions before the affected tool batch continues.

`.qignore` affects discovery and root listings. It is not an access-control boundary: a tool can still read an explicitly requested in-workspace path.

## Shell execution

Shell commands start inside the workspace but are **not** an operating-system sandbox. A command can exercise the permissions of the q process unless your operating system, container, or account limits it.

Use an isolated account, container, or virtual machine when the repository or requested command is untrusted.

## Credentials

Prefer environment-variable references for provider and MCP credentials. q stores global configuration under `~/.q`; Windows file modes do not manage ACLs, so protect that directory with the account and filesystem controls appropriate to your machine.

Remote API keys and Gateway keys are separate. Possession of a Remote key can authorize work in any directory the q process account can access.

## Network services

Internal data services bind to loopback and do not require bearer authentication. The standalone Gateway and Remote service can be configured separately.

Do not expose an unauthenticated Gateway or Remote listener on a non-loopback address. Remote has no built-in TLS or path allowlist; use authentication plus a trusted network or a correctly configured reverse proxy.
