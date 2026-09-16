---
title: Install q
description: Build q from source and start it in the workspace you want it to understand.
sectionLabel: Guide
toc:
  - id: requirements
    label: Requirements
  - id: install-from-source
    label: Install from source
  - id: run-without-installing
    label: Run without installing
  - id: start-in-a-workspace
    label: Start in a workspace
---

## Requirements

Before installing q, make sure the following are available:

- Go 1.26.5 or later
- Git on `PATH`
- A terminal with ANSI color support
- Credentials for at least one model provider, or a working local compatible endpoint

[Task](https://taskfile.dev/) is optional. Every required build and test command can run directly through Go.

## Install from source

Clone the repository, enter it, and install both q commands:

```powershell
git clone https://github.com/snowmerak/q.git
cd q
go install ./cmd/q ./cmd/q-mcp
```

`q` is the interactive agent and standalone service host. `q-mcp` exposes q's workspace tools to another MCP client over stdio.

## Run without installing

From a source checkout, start q directly:

```powershell
go run ./cmd/q
```

The repository also includes Task targets for common development operations:

```powershell
task run
task build
task test
```

## Start in a workspace

Change to the repository or directory q should treat as its workspace, then run q:

```powershell
cd C:\path\to\project
q
```

On first launch, q opens provider setup. Once a model is assigned, type a request normally or type `/` to browse commands.
