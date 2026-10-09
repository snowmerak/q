---
title: Install q
description: Install q with Go or build it from source, then start the local Studio.
sectionLabel: Guide
toc:
  - id: requirements
    label: Requirements
  - id: install-with-go
    label: Install with Go
  - id: install-from-source
    label: Install from source
  - id: run-without-installing
    label: Run without installing
  - id: start-studio
    label: Start Studio
---

## Requirements

Before installing q, make sure the following are available:

- Go 1.26.9 or later
- Git on `PATH`
- A modern browser for Studio
- Credentials for at least one model provider, or a working local compatible endpoint

[Task](https://taskfile.dev/) is optional. Every required build and test command can run directly through Go.

## Install with Go

Install q directly from its Go module:

```powershell
go install github.com/snowmerak/q/cmd/q@latest
```

The optional `q-mcp` companion exposes q's workspace tools to another MCP client over stdio:

```powershell
go install github.com/snowmerak/q/cmd/q-mcp@latest
```

Go writes the binaries to `GOBIN`, or to `GOPATH/bin` when `GOBIN` is unset. Make sure that directory is on `PATH`.

## Install from source

Clone the repository when you want to build the current source tree or contribute to q:

```powershell
git clone https://github.com/snowmerak/q.git
cd q
go install ./cmd/q ./cmd/q-mcp
```

This installs both commands from the checked-out source rather than resolving `@latest` through the Go module proxy.

## Run without installing

From a source checkout, start q directly:

```powershell
go run ./cmd/q studio
```

The repository also includes Task targets for common development operations:

```powershell
task run
task build
task test
```

## Start Studio

Start Studio from any directory, then register repositories from **Sessions**:

```powershell
q studio
```

Open **Settings → Providers** to add a provider and **Settings → Models** to assign the default model. Bare `q` remains available as a terminal compatibility client.
