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
  - id: stream-events
    label: Stream events
  - id: errors-and-capacity
    label: Errors and capacity
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

The prompt is limited to 32 KiB of UTF-8 data, and the complete JSON request body is limited to 256 KiB. Unknown JSON fields are rejected. The response uses `Cache-Control: no-store`, and the `X-Q-Session-ID` header exposes the selected session.

## Stream events

The first NDJSON record is always `session`. Its `working_directory`, `session_id`, and `created` fields identify the canonical workspace and whether q created a new session.

Events between `session` and the terminal record expose the same execution already produced by q's agent runtime:

| Type | Important fields | Meaning |
| --- | --- | --- |
| `status` | `detail` | Startup warning or concise runtime status. |
| `activity` | `agent`, `task_id`, `parent_id`, `action`, `detail` | Subagent lifecycle progress. |
| `trace` | `agent`, `kind`, `call_id`, `name`, `content`, `is_error` | Detailed subagent trace entry. |
| `tool_call` | `call_id`, `name`, `content` | Main-agent tool request; `content` contains its arguments. |
| `message` | `role`, `name`, `call_id`, `content`, `is_error` | Model or tool message appended to the session. |
| `question` | `question`, `context` | An attempted interactive question; Remote cannot accept an answer. |
| `result` | `session_id`, `outcome`, `content` | Successful terminal result. |
| `cancelled` | — | Terminal record after cancellation. |
| `error` | `error` | Terminal failure after streaming has begun. |

Clients should ignore fields they do not need and continue reading until a terminal record. Disconnecting cancels the request context and therefore the active run.

## Errors and capacity

Before streaming begins, failures use a JSON `error` object and an HTTP status. Common responses are:

| Status | Code | Meaning |
| --- | --- | --- |
| `400` | `invalid_request` | Invalid JSON, directory, session ID, content type, or request shape. |
| `401` | `invalid_api_key` | Authentication is enabled and the bearer key is missing or invalid. |
| `404` | `session_not_found` or `subagent_not_found` | The selected durable session or subagent does not exist. |
| `409` | `session_busy` | Another process already owns the selected session. |
| `413` | `request_too_large` | The body or prompt exceeds its bound. |
| `429` | `remote_capacity` | Active runs have reached `agents.max_parallel`. |
| `503` | `subagent_unavailable` | The requested agent exists but its configured runtime is unavailable. |

`GET /v1/health` reports the service version and whether it is currently accepting runs. Both that endpoint and `/openapi.json` are intentionally available without a bearer key and expose no workspace state.

## Remote boundaries

Remote preserves `ask_to_user` in the tool catalog, but the stream is one-way. q emits the attempted `question`, then returns an interaction-unavailable tool error immediately; the model may continue with available information or finish blocked.

Remote does not provide built-in TLS, path allowlists, background jobs, reconnect, or interactive answers. Keep it on loopback unless authentication and a trusted confidential network or reverse proxy are in place.

The running service exposes its exact contract at `/openapi.json`.
