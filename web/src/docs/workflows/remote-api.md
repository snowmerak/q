---
title: Remote API
description: Run q sessions and one-shot agent requests over an authenticated foreground HTTP service.
sectionLabel: Guide
toc:
  - id: configure-and-start
    label: Configure and start
  - id: discover-workspace-state
    label: Discover workspace state
  - id: run-an-agent
    label: Run an agent
  - id: remote-boundaries
    label: Remote boundaries
---

## Configure and start

Configure the listener, authentication switch, and Remote-only API keys:

```powershell
q remote config
```

Then run the foreground host:

```powershell
q remote
```

The default listener is `127.0.0.1:0`. Remote keys are separate from Gateway keys because Remote can select any working directory accessible to the q process and can run workspace-mutating tools.

## Discover workspace state

List sessions for a working directory:

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/sessions?working_directory=C%3A%5Cwork%5Cproject"
```

List the built-in and custom subagents effective for the same workspace:

```powershell
curl.exe -H "Authorization: Bearer $env:Q_REMOTE_KEY" "http://127.0.0.1:8080/v1/subagents?working_directory=C%3A%5Cwork%5Cproject"
```

## Run an agent

`POST /v1/subagent-runs` accepts JSON and streams `application/x-ndjson` events:

```powershell
curl.exe -N `
  -H "Authorization: Bearer $env:Q_REMOTE_KEY" `
  -H "Content-Type: application/json" `
  -d '{"working_directory":"C:\\work\\project","prompt":"Explain the startup path"}' `
  http://127.0.0.1:8080/v1/subagent-runs
```

`working_directory` and `prompt` are required. Omit `session_id` to create a session; provide it to resume an existing unoccupied session. Omit `subagent` or send an empty value to run the default main loop. Set it to an ID such as `builtin/scout` to use the direct subagent flow.

The response begins with a `session` event and ends with `result`, `cancelled`, or `error`. The `X-Q-Session-ID` response header exposes the selected session.

## Remote boundaries

Remote preserves `ask_to_user` in the tool catalog, but the stream is one-way. A call returns an interaction-unavailable tool error immediately; the model may continue with available information or finish blocked.

Remote does not provide built-in TLS, path allowlists, background jobs, reconnect, or interactive answers. Keep it on loopback unless authentication and a trusted confidential network or reverse proxy are in place.

The running service exposes its exact contract at `/openapi.json`.
