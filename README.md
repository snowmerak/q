# q

`q` is a workspace-native terminal agent built with Bubble Tea. It combines a
multi-provider chat client with streamed reasoning and responses, approval-gated
planning and execution, isolated subagents, ordered model fallback, durable
workspace history, a shared Library for Agent Skills and propositions,
read-only language-server queries, Loom artifacts, and an interactive Git
commit workflow.

`q` embeds [`snowmerak/llm-provider`](https://github.com/snowmerak/llm-provider)
and supervises its OpenAI-compatible Gateway as a child of the current `q`
executable.

## Quick start

q requires Go 1.26.5 or later.

The repository's `go.work` connects q, the Q-maintained ACP SDK fork, and its
generator for source builds. Keep workspace mode enabled when developing them
together. The SDK is a separately versioned module, not a root `go.mod` replacement.
See [ACP fork maintenance and release](docs/acp-go-sdk-patch.md) for the first SDK
publication, upstream updates, and isolated installation checks.

Run from source:

```powershell
go run ./cmd/q
```

With [Task](https://taskfile.dev/) installed:

```powershell
task install  # install q and q-mcp into the Go binary directory
task build    # build both commands into ./bin
task run      # run q from source
task test     # test q, the ACP SDK, and its generator
task dist:check # check versioned installation without publishing
```

Run `q` inside the directory you want to use as the workspace. On startup, q
waits up to 1.5 seconds for initial model discovery. If discovery finishes
sooner, the TUI opens immediately; otherwise the same initialization continues
in the background after the TUI renders.

Type `/` at the start of the chat input to open command completion. Keep typing
to filter, use Up/Down to select, and Tab or Enter to complete the command.
Enter runs a fully typed command; Escape closes the popup without quitting.
The popup overlays the transcript without moving the input or its IME cursor.

### Agent Client Protocol (ACP)

Run q as an ACP agent over stdin/stdout, without the Bubble Tea UI:

```powershell
q acp [--root <workspace-path>]
```

When `--root` is omitted, q uses the process's current working directory.

Each active session owns a session-scoped lock, an independent conversation and
learning runtime, and its selected `.q/sessions/<uuid>/session.json` projection.
Durable archive records and their indexes are accessed through a lease on the
user-level Workspace Memory service. It supports ACP `session/new`,
`session/prompt`, `session/cancel`, streamed message/thought updates, tool-call
updates, task-lifecycle plan updates, `session/list`, `session/delete`, and
`session/close`. Session metadata uses the first meaningful prompt as its title
and records the latest turn time. The `plan`, `agent:search`, `commit`, `learn`,
`clear`, and `help` commands are advertised to ACP clients and handled without
the Bubble Tea UI. `/plan <request>` runs the same Griller, Planner,
approval-gated Coder, and review
workflow as the TUI while projecting bounded progress and plan status through
ACP updates. Because execution always requires explicit approval, the ACP
client must advertise form elicitation support. `/agent:search <query>` creates
an isolated session on the configured ACP Search agent, returns its evidence
report, and then deletes or closes that session. `/clear` replaces q's persisted
conversation projection with an empty state while keeping the active ACP session
ID usable; `/new` creates another session. `/commit` shows the generated commit messages and their staged files
through form elicitation, then accepts commit, commit-and-push, regenerate, or
cancel. Clients without form elicitation support cannot run the commit workflow.

One ACP process is bound to one workspace and may keep multiple ACP sessions
active concurrently. `session/list` returns all
persisted projections. `session/load` replays a selected session,
`session/resume` reconnects without replay, `session/close` releases its session
lock, in-memory conversation, and session-provided MCP connections, while
`session/delete` removes only the selected conversation and plan
projections. Durable archive records and project files are preserved.

Client-supplied stdio and Streamable HTTP MCP servers are connected in a
session-owned overlay over q's configured MCP catalog and closed with that
session, so concurrently active sessions cannot replace each other's routes.
Stdio MCP connection, tool-discovery, and RPC failures include a bounded recent
stderr tail in the error returned to the ACP client. Configured environment and
header values and conventional inherited credential variables are redacted.
Normal stderr logs do not by themselves fail a request, and subprocess stderr
is never copied into the ACP stdout stream.
SSE remains unsupported. ACP image prompts are advertised for OpenAI-compatible, OpenRouter,
and xAI/Grok routes; native Anthropic and Codex routes remain text-only until the
provider layer has a common multimodal representation. Provider setup must
already exist before starting `q acp`.

Configure ACP agents for q's external Search role with the Agents settings
screen:

```powershell
q agents
```

The screen manages named ACP process profiles and assigns one profile to the
`search` role. The Codex preset runs an installed `codex-acp`, or falls back to
`npx -y @agentclientprotocol/codex-acp`; the adapter translates ACP to the
Codex App Server and reuses Codex authentication. The Grok preset runs the Grok
Build CLI's native `grok agent stdio` transport. Custom stdio ACP commands are
also supported.

When Griller or Planner calls `external_search`, q resolves
`agents.roles.search.agent`, starts that ACP process, initializes it, creates an
isolated session, sends the bounded research request, and returns the final
report and source URLs as the tool result. q then calls `session/delete`, or
`session/close` when deletion is not advertised, and terminates the process.
Only read, search, fetch, and think permission requests can be allowed once;
edit, delete, move, and execute requests are rejected. The agent therefore uses
its own authenticated subscription and search capabilities without becoming a
persistent workspace writer.

Select a connection in `q agents` and press `c` to test it. The probe performs
ACP initialization, creates an isolated session, deletes or closes it, and then
terminates the process without sending a model prompt.

### Standalone Gateway

Run only the configured Gateway, without the TUI or workspace services:

```powershell
q gateway [--host <ip>] [--port <port>]
q gateway config
```

The command reads providers from `~/.q/providers.json` and listener/API-key
settings from `~/.q/gateway.json`. The initial default is `127.0.0.1:0`, so the
OS selects an available port. A saved nonzero port is used when available and
falls back to port `0` on a collision; an explicit conflicting `--port` remains
an error. Command-line `--host` and `--port` values override the saved defaults.
The effective OpenAI-compatible `/v1` endpoint is printed after startup.

When at least one Gateway API key is active, standalone clients must send one
as an `Authorization: Bearer` credential. With no active keys, `q gateway`
accepts requests without authentication. `q gateway config` manages network
defaults, keys, and providers; plaintext keys are shown only once, while
`gateway.json` stores keyed BLAKE3 digests. Key changes, including transitions
between authenticated and unauthenticated operation, are reloaded without a
restart. Provider and listener changes require a restart. Do not expose a
no-key Gateway on a non-loopback address unless the surrounding network is
already trusted and access-controlled.

### Global Library

Run the global Library as a dedicated foreground service or configure its bind
host and fixed rendezvous port:

```powershell
q library
q library config
```

The configuration screen is also available as `/library`. Defaults are saved
independently in `~/.q/library.json`; a running leader must be restarted before
listener changes take effect. Unlike the Gateway's optional port `0`, the
Library requires a stable port from `1` to `65535` so other q processes can
find the same service.

Ordinary `q` sessions ensure that the same Library is available. When no
compatible server is running, one session wins the user-level file lock and
embeds the server until that q process exits. Other sessions connect over HTTP;
use `q library` when its lifetime should be independent of a workspace TUI.

### Workspace Memory

Run Workspace Memory as a dedicated foreground service:

```powershell
q memory
```

Workspace Memory is a user-level, loopback-only HTTP service that owns each
open workspace's durable records and Bleve/HNSW indexes. Ordinary `q`, `q acp`,
and `q-mcp` processes ensure the service is available automatically; `q memory`
keeps a leader or takeover participant alive independently of those clients.
The default endpoint is `127.0.0.1:17892`. Requests are authenticated with an
automatically generated bearer token stored in `~/.q/workspace-memory.token`
with mode `0600` on POSIX.

Canonical workspace roots are multiplexed through one service. Each client
holds a renewable lease, and the server closes that workspace's records and
indexes after the final lease is released or expires. See
[Workspace Memory](docs/workspace-memory.md) for ownership and transitional
locking details.

During leader handoff, clients make a bounded five-second attempt to reopen the
same workspace lease and retry the interrupted operation. Record IDs are
assigned before transport, so retrying a mutation after a lost response does
not create a duplicate archive record.

### Standalone settings screens

The remaining settings screens can be opened without starting the main chat
UI. Each command loads only the resources its screen needs:

```powershell
q model   # managed Gateway + model discovery + current-workspace overrides
q agents  # ACP agent connections + external Search role assignment
q mcp     # external MCP tool servers + per-role assignments
q skills  # global and current-workspace Agent Skills
q ignore  # current workspace's .qignore editor
q lsp     # global language servers and current-workspace project roots
q help    # scrollable command and key reference
```

Workspace Memory uses the Session Store's pure-Go HNSW index, so q does not
require CGO, FAISS, or a vector-specific build tag.

## Runtime layout

An ordinary `q` session coordinates five components with different lifetimes:

| Component | Scope and ownership |
|---|---|
| Main TUI | Owns one active conversation under `.q/sessions/<uuid>/`, its session lock, task lifecycle, and session-only `learn` tool. |
| Managed Gateway child | Aggregates configured providers for that q process. It binds to loopback on an ephemeral port and uses a parent-injected temporary bearer key. |
| Workspace process runtime | Owns LSP sessions and file tools locally. Loom remains shared on disk and coordinates artifact operations, GC root scans, and session projection mutations through `.q/loom/operation.lock`. |
| Workspace Memory | A user-level loopback HTTP service that multiplexes canonical workspace roots and exclusively owns their durable records, Bleve indexes, and HNSW indexes while leases are active. |
| Global Library | Owns global Agent Skill projections, propositions, their search indexes, and the serialized proposition-judging queue under `~/.q/library/`. One process leads; other q processes connect over HTTP. |

`q gateway` is a separate, user-addressable Gateway process. It uses the saved
host and port and requires bearer authentication only while at least one API
key is active. `q-mcp` exposes the workspace MCP surface over stdio without
starting the chat TUI or owning its learning cursor.

## First launch

On first launch, q asks for a provider ID, optional model prefix, API type,
optional provider kind, endpoint, and API-key source. The prefix defaults to
the provider ID. Provider settings remain editable even when an upstream is
offline.

Personal configuration is stored in:

```text
~/.q/config.yaml
~/.q/providers.json
~/.q/gateway.json
~/.q/gateway.key
~/.q/library.json
~/.q/library.key
~/.q/workspace-memory.json
~/.q/workspace-memory.token
~/.q/mcp.json
```

Prefer environment variables over inline API keys. On POSIX, q creates the
configuration directory with user-only permissions and writes configuration
files with mode `0600`. Windows file modes do not manage ACLs.

## TUI reference

The regular chat footer stays compact. Use `Ctrl+H` or `/help` from any screen
to open the complete, scrollable guide. Closing help restores the previous
screen without discarding editor state or interrupting an active turn.

### Slash commands

| Command | Action |
|---|---|
| `/plan [request]` | Grill, research, approve, and execute a work plan. Without an inline request, the next message becomes the request. |
| `/agent:search <query>` | Explicitly run the configured ACP Search agent in an isolated read-only session and return its evidence report. |
| `/commit` | Open the interactive commit workflow, then return to chat. |
| `/sessions` | List workspace sessions by title and recent activity, then resume one or create another. |
| `/new` | Create and switch to a new workspace session without deleting the previous one. |
| `/model` | Configure the workspace/default, embedding, subagent role, and grouped fallback models. |
| `/gateway` | Configure Gateway network defaults, API keys, and providers. |
| `/library` | Configure the global Library's default host and fixed port. |
| `/loom` | Inspect Loom usage and configure or run garbage collection. |
| `/ignore` | Edit workspace discovery rules in `.qignore`. |
| `/skills` | Interactively add, pull, remove, and reindex global/session Agent Skills. |
| `/lsp` | Configure global language servers and current-workspace project roots. |
| `/mcp` | Configure external MCP tool servers and per-role assignments. |
| `/clear` | Empty the current session's conversation and plan checkpoint while retaining its session ID. |
| `/learn` | Close the current learning segment and enqueue Thinker extraction when workspace learning is enabled. |
| `/learn off` | Disable conversation collection and Thinker queue processing for this workspace. |
| `/learn on` | Re-enable workspace learning and resume any previously queued segments. |
| `/learn status` | Show whether learning is enabled for this workspace. |
| `/help` | Open the command and shortcut guide. |

### Chat keys

| Key | Action |
|---|---|
| `Enter` / `Ctrl+S` | Send. |
| `Shift+Enter` | Insert a newline. |
| `Ctrl+L` | Clear the current chat projection. |
| `Ctrl+P` | Open Gateway settings. |
| `Ctrl+H` | Open or close help. |
| `Ctrl+G` | Expand or collapse the detailed subagent trace when available. |
| `Ctrl+C` | Interrupt the active turn; quit while idle. |
| `Esc` | Leave the current screen or quit chat. |

Assistant responses render as terminal Markdown using the detected terminal
theme and current viewport width. OpenAI-compatible, OpenRouter, xAI/Grok, and
Codex routes stream reasoning and response deltas into the chat; reasoning is
shown separately from the assistant response, while streamed tool-call
fragments are assembled and executed through the ordinary tool lifecycle.
Native Anthropic chat retains its one-shot behavior. A model group streams only
when every candidate supports chat streaming.

For OpenAI-compatible routes, q combines the leading system and developer
instruction block into one wire-level system message. Internal instruction
boundaries remain separate in workspace state, while strict local Jinja chat
templates that permit a system message only at the beginning receive a
compatible request shape.

User input and tool output remain literal. Outside inline code and fenced code
blocks, common display-math commands such as `$\rightarrow$`, `$\leq$`, and
`$\infty$` are rendered as their terminal-friendly Unicode symbols.

## Planning and execution

`/plan` is an explicit orchestration mode. It does not modify the workspace
until the combined conditions and plan have been approved.

```text
request
  -> Griller questions and repository research
  -> Scout non-mutating investigations when evidence is needed
  -> Planner conditions, targets, tasks, and verification
  -> user approval
  -> Coder task
  -> Planner review: retry or next
  -> completion
```

The Griller asks the user only for decisions the repository cannot answer. It
can delegate bounded, non-mutating questions to Scout while building the
planning brief. Scout can run information-gathering commands for environment,
architecture, and tool-version facts, but its contract forbids workspace
changes, builds, tests, generators, dependency changes, and Git mutations.
Scout returns structured findings directly; large supporting results remain
available through Loom references.

Each approved task selects its files using an OR-of-AND target condition made
from static paths and optional `loom_eval` transforms. The condition selects a
file set only—it does not encode execution order or result acceptance. The
Coder receives the full current Plan, resolved targets, and any retry feedback.
After every attempt, the Planner chooses `retry` or `next` and may add newly
verified facts to the Plan.

Coder tool calls automatically contribute bounded review evidence: tool name,
Loom reference, error state, and relevant workspace paths, without copying raw
command output or the complete Coder transcript into the review prompt. The
Planner can inspect selected files and Loom artifacts, query propositions and
read-only LSP data, or run non-mutating verification commands before accepting
the task or returning actionable retry feedback.

Execution state is atomically checkpointed in
`.q/sessions/<uuid>/plan-execution.json`. On the
next launch, interrupted work offers Resume, Inspect, and Discard. Resuming a
Coder that may already have changed files starts a new recovery attempt that
first inspects the workspace; a saved Coder result resumes at Planner review.
Discard removes the checkpoint but never reverts workspace changes.

See [plan orchestration](docs/plan-orchestration.md) and
[execution orchestration](docs/execution-orchestration.md) for the detailed
contracts.

## Commit workflow

Use `/commit` from the main TUI or an ACP client with form elicitation support,
or run the standalone command inside a Git worktree:

```powershell
q commit
```

The workflow creates Oh My Pi-style Conventional Commits with the configured
`commit` model:

- Existing staged changes are used. If the index is empty, q stages the working
  tree while excluding q's root `.q` metadata.
- Whitespace-only and import-order-only changes bypass the model and use
  `style: formatted code` or `style: reorganized imports`.
- Ordinary changes run in an isolated commit session with commit-specific tools
  and restricted Loom access.
- The agent must begin with `git_overview`; full diffs stay outside the initial
  prompt and are fetched by file or hunk only when needed.
- A validated `propose_commit` or `split_commit` tool call is required. After
  three missing-proposal reminders, q uses a mechanical fallback.
- Every proposal opens a review before Git changes. Split proposals expose
  read-only file groups; file and hunk reassignment are intentionally omitted.
- The captured index is checked again immediately before commit so external
  changes cannot be approved accidentally.

Review keys are `Up`/`Down` to select a split message, `e` to edit it, `Ctrl+S`
to validate and save the edit, `r` to regenerate, `Enter` to commit, `p` to
commit and push, and `Esc` to cancel. Push requires an existing upstream.

## Providers and models

`providers.json` uses `llm-provider/gateway.Config`. Multiple enabled providers
share one local Gateway and expose models with their provider ID or configured
prefix:

```json
{
  "listen": "127.0.0.1:0",
  "providers": [
    {
      "id": "codex",
      "type": "codex",
      "enabled": true
    },
    {
      "id": "local",
      "type": "openai-compatible",
      "kind": "generic",
      "enabled": true,
      "base_url": "http://localhost:1234/v1",
      "models": ["qwen3-32b"],
      "model_metadata": {
        "qwen3-32b": {
          "context_length": 131072
        }
      }
    }
  ]
}
```

The optional `kind` separates the HTTP transport from the backend semantics.
It is especially useful when a proxy exposes several providers through one
OpenAI-compatible API:

| Kind | Gateway behavior |
|---|---|
| omitted | Infer from `type`; an `openai-compatible` `api.x.ai` URL is inferred as Grok, and other compatible URLs default to OpenAI semantics. |
| `generic` | Send no provider-specific prompt-cache extension. Use this for strict or unknown OpenAI-compatible servers. |
| `openai` | Use OpenAI `prompt_cache_key` affinity. |
| `openrouter` | Use OpenRouter `session_id` sticky routing. |
| `grok` / `xai` | Use `X-Grok-Conv-Id` affinity and known xAI model capabilities. |
| `anthropic` / `claude` | Use Anthropic automatic top-level `cache_control`. |
| `codex` / `codex-app-server` | Keep provider-managed conversation state. |

Only `type: "openai-compatible"` accepts every kind. Native Anthropic, Grok,
Codex, and OpenRouter transports accept only their matching kind; leaving it
empty selects the matching behavior automatically. The provider editor exposes
only valid choices and resets an incompatible explicit kind to automatic when
the API type changes.

OpenRouter-hosted Claude models intentionally receive only `session_id` sticky
routing; q does not add Anthropic `cache_control` automatically on an OpenRouter
route. Use a native Anthropic route for automatic Claude prompt caching, or add
an explicit `body.cache_control` value in `providers.json` when OpenRouter is
required.

Static `models` remain available even when metadata discovery times out.
`model_metadata` overrides upstream metadata before prefixes are applied. In
the `/model` catalog, `Ctrl+E` sets or clears a model's Gateway
`context_length` override.

The managed Gateway used by an ordinary `q` session binds only to
`127.0.0.1:0` and reports the assigned port to its parent over a private stdout
handshake. Provider edits start a replacement child before saving and
activating it; a failed replacement leaves the running Gateway untouched. This
is separate from `q gateway`, whose listener uses `gateway.json` and command-line
overrides.

Each supervised Gateway generation also receives a newly generated temporary
API key. The parent `q` client uses that key automatically, and it is neither
written to `providers.json` nor retained in the child environment after
startup. Replacing the Gateway rotates both its endpoint and API key.

That supervised child credential is independent of the persistent keys used by
standalone `q gateway`: the parent process injects the temporary key into its
own clients automatically, so launching or replacing the child does not require
the user to copy a key.

`/model` shows a Role × Scope assignment table with `GLOBAL` and `WORKSPACE`
columns. Use Left/Right to choose a scope, Up/Down to choose a role, Enter to
change the selected assignment, and `i` to clear it. Workspace overrides for
the default main model and non-shared roles are stored in `.q/model.json`; an
empty workspace cell inherits its active/global assignment. Thinker, Librarian,
and Embedding have disabled workspace cells because their state is shared
across workspaces. Workspace overrides store only the role-to-model assignment;
context length and other model metadata always come from the global Gateway
catalog. Version 1 workspace model files are accepted, but their legacy
`context_window` cache is discarded. An empty
reasoning effort lets the provider choose its default; explicit
efforts are accepted only when advertised by that model. The built-in roles are
`griller`, `scout`, `research`, `planner`, `coder`, `commit`, `advisor`,
`thinker`, and `librarian`. The `thinker` role performs durable proposition
selection and extraction after successful conversation turns. The `librarian`
role independently judges whether each extracted proposition should be created,
merged, or discarded.

Roles may also reference an ordered model group. Each candidate can override
the role's reasoning effort and can set an optional timeout. q tries the first
candidate and falls back immediately on a candidate timeout or HTTP 5xx error.
Other HTTP errors, tool failures, user cancellation, and structured-output
validation failures do not trigger model fallback. Three consecutive transient
failures open that model's in-memory circuit for 30 seconds; one half-open probe
is allowed after the cooldown. Circuit state is process-local and is not
persisted. A successful fallback remains selected for the rest of that agent
execution; a new execution starts from the first candidate whose circuit allows
a request. Press `g` on `/model`'s top-level target screen to create and edit
groups, add candidates, choose per-candidate effort and timeout, reorder the
fallback chain, or delete an unreferenced group. Groups can also be declared in
`~/.q/config.yaml`.

Inside the group editor, `a` adds a candidate, `Enter` edits the selected
candidate, `Ctrl+Up`/`Ctrl+Down` changes fallback order, `d` removes a
candidate, and `s` validates and saves the group. Saving a group does not assign
it to any role; choose the group separately from the relevant role's model
picker.

The managed and standalone Gateways expose each group as `group/<name>` in
`/v1/models` and accept that ID in chat-completion requests. The advertised
context length and maximum output are the smallest corresponding limits among
all candidates; an unknown member limit makes the group limit unknown. Agent
role pickers render configured groups as `group:<name>` choices. Gateway group
configuration is read for each request, so editing a group does not require a
Gateway restart.

The embedding picker accepts dimensions from 1 to 4096. Because the shared
model catalog does not identify embedding-only models, it currently shows all
models. The selected model powers both global proposition retrieval and
workspace-history embedding. Press `i` to clear an embedding or role override
and restore inheritance.

## Configuration example

```yaml
version: 1
provider:
  managed: true
  model: codex/gpt-5.6-terra
  system_prompt: You are a helpful assistant.
embedding:
  model: openai/text-embedding-3-small
  dimensions: 1536
context:
  trigger_ratio: 0.85
  target_ratio: 0.22
  recent_ratio: 0.07
model_groups:
  heavy:
    candidates:
      - model: codex/gpt-5.6-sol
        reasoning_effort: high
        timeout: 60s
      - model: grok/grok-4.5
        reasoning_effort: high
        timeout: 90s
agents:
  max_parallel: 3
  connections:
    codex-search:
      preset: codex
    grok-search:
      preset: grok
      disabled: true
  roles:
    planner:
      group: heavy
    coder:
      model: codex/gpt-5.6-terra
      reasoning_effort: medium
    commit:
      model: codex/gpt-5.6-terra
      reasoning_effort: low
    search:
      agent: codex-search
loom:
  maximum_artifact_mib: 64
  maximum_store_mib: 256
  gc:
    trigger_ratio: 0.8
    target_ratio: 0.6
    grace_hours: 1
lsp:
  servers:
    gopls:
      languages: [go]
      command: gopls
      args: [serve]
    typescript:
      languages: [typescript, javascript]
      command: typescript-language-server
      args: [--stdio]
  languages:
    go: gopls
    typescript: typescript
    javascript: typescript
```

## Workspace state and locking

Global provider and model choices stay under `~/.q`. Conversation and execution
state are local to the directory where q starts:

| Path | Purpose |
|---|---|
| `.q/sessions/<uuid>/session.json` | One session's transcript, compacted request-context projection, run identity, title/activity metadata, active task lifecycle, and durable Thinker learning queue. |
| `.q/model.json` | Per-role workspace model overrides. Model metadata remains in the global Gateway configuration. It is preserved by `/clear`. |
| `.q/learning.json` | Workspace learning disable switch. Existing queued segments are preserved while learning is disabled, and the setting is preserved by `/clear`. |
| `.q/lsp.json` | Current workspace's language project roots and optional server overrides. |
| `.q/sessions/<uuid>/plan-execution.json` | Durable checkpoint for that session's approved plan execution. |
| `.q/data/records/` | Source records for durable workspace history. |
| `.q/index/bleve/` | Derived full-text index. |
| `.q/index/vectors.hnsw`, `.q/index/vectors.ids.json`, `.q/index/state.json` | Derived semantic index and rebuild state when embeddings are available. |
| `.q/loom/` | Immutable, content-addressed tool artifacts. |
| `.q/loom/operation.lock` | Short-lived OS lock serializing shared Loom reads/writes, root scans, session projection mutations, and garbage collection. |
| `.q/loom/leases/` | Expiring transient roots that protect declared `loom_eval` inputs across the worker-process boundary. |
| `.q/skills/` | q-managed project Agent Skill checkouts. |
| `.q/session-locks/<uuid>.lock` | Diagnostic metadata for the process currently owning a session. |
| `.q/workspace.lock` | Legacy/standalone workspace-owner metadata, also used briefly while migrating the old single-session layout. |
| `.q/workspace-memory.lock` | Diagnostic metadata for the Workspace Memory server that currently owns this workspace's records and indexes. |
| `.qignore` | Workspace discovery exclusions. |

OS-backed locks protect independent ownership boundaries.
Workspace Memory alone holds `.q/workspace-memory.lock` while at least one
lease exists for the canonical root; this makes it the sole process that opens
or rebuilds the shared record, Bleve, and HNSW state. The interactive app and
`q acp` hold only their selected session lock: opening the same UUID twice is
rejected, while different UUIDs can run together. Loom takes its operation lock
around each filesystem transaction; session and plan projection writes share
that lease so a root cannot change between GC's scan and sweep. `q-mcp` no
longer owns the legacy workspace lock for its process lifetime.

The first new-version launch migrates `.q/session.json` and
`.q/plan-execution.json` into the UUID layout while briefly coordinating with
`.q/workspace.lock`, even when an older process has not yet written its first
projection. Migration also holds each destination session lock through publish
and legacy cleanup. If a retry finds divergent data at the preferred ID, both
versions are preserved by placing the legacy projection in a deterministic
conflict session. This is a one-way layout upgrade for older q binaries.

Lock-file existence is never a sentinel. Each OS lock lives on an open file handle;
after a crash or forced termination the handle closes automatically, while the
file remains as reusable diagnostic metadata. Workspace Memory does not
serialize or detect concurrent edits to project files; coordinating overlapping
file mutations remains the responsibility of the sessions involved.

The main TUI starts with a newest-first session picker that shows each saved
title and activity time. It acquires and revalidates only the selected session's
lock; `/sessions` returns to the same picker later. `/clear` writes an empty
projection for the same session UUID. `/new` creates a different UUID. Durable
archive records and workspace file changes remain intact.

## Tools and task lifecycle

Each chat request exposes q's workspace-scoped builtin MCP tools. Developer
context describes the host OS, architecture, workspace root, and exact command
shell so generated commands use appropriate quoting. Filesystem tools are
root-jailed; shell commands start in the workspace but are not an OS sandbox.

The core surface covers anchored file reads and edits, complete-file writes,
directory listing and creation, path copy/move/removal, asynchronous commands,
and Loom inspection. Optional runtime services add archive, Agent Skill,
proposition, and read-only LSP queries. Every non-Loom result passes through
Loom's bounded capture layer before it returns to the model.

The main agent also receives orchestration tools:

- `task_start` begins a tool-using or multi-step task.
- `task_complete` is required exactly once after a started task reaches a
  terminal outcome.
- `ask_to_user` pauses the current turn for a required decision. Choices return
  their stable ID; typing sends a free-form answer instead.

An accepted `task_start` is written immediately to the active session's
`session.json`. If its
turn is interrupted or q exits before `task_complete`, the objective,
completion criteria, and start time are restored on the next launch. The model
continues the existing lifecycle without calling `task_start` again; a
successful `task_complete` or `/clear` removes it.

The in-process chat runtime also exposes `learn`, which closes the current
conversation's learning segment. The standalone `q-mcp` command deliberately
omits it because an external MCP process does not own the active chat cursor.

Builtin tools are ordered by function name before they enter a model request;
external MCP server IDs and their imported tools are ordered as well. This
keeps tool-bearing prompt prefixes stable across q restarts. MCP object schemas
with no input fields are normalized to include `properties: {}` for strict
OpenAI-compatible validators.

### External MCP tools

Use `q mcp` to define external MCP servers and assign them to q roles. The same
screen is available as `/mcp` in chat. `default` is the main chat role; the
remaining entries are the tool-using `griller`, `scout`, `planner`, and `coder`
roles. Assignments stay stable when a role's model or fallback group changes.
`q mcp` is the configuration command and does not need Gateway model discovery;
`q-mcp` remains q's separate workspace MCP server.

External profiles are stored in `~/.q/mcp.json`. Stdio profiles contain a
command and argument array; Streamable HTTP profiles contain an absolute URL.
Credentials are never stored inline: child environment variables and HTTP
headers map to environment-variable names in q's parent process. For example:

```json
{
  "version": 1,
  "servers": {
    "docs": {
      "transport": "streamable-http",
      "url": "https://example.test/mcp",
      "headers": { "Authorization": "DOCS_MCP_AUTH" }
    }
  },
  "roles": {
    "default": ["docs"],
    "scout": ["docs"]
  }
}
```

Only MCP tools are imported. A server is exposed only to the roles it is
assigned to. Exposed names are namespaced as
`mcp_<server-id>__<tool-name>` to prevent collisions. Every external result is
captured by Loom before its bounded receipt is returned to the model. Connection
or discovery failure for one server does not disable builtin tools or other MCP
servers. Stdio children run with the active workspace as their working directory
and are closed when q exits or applies replacement MCP settings.

Tool calls, arguments, results, command status, and intermediate agent notes can
be inspected in the live detailed trace. Interrupting a turn cancels its request
context, records unfinished tool calls as cancelled, persists the cancellation,
and returns to the input prompt.

## Agent Skills

q supports the [`SKILL.md` Agent Skills format](https://agentskills.io/specification)
without injecting the complete skill catalog into model context. Global skill
metadata is projected into q Library, while project metadata is projected into
Workspace Memory records; both are indexed by Bleve. The main chat, Griller,
and Scout can use `search_skills`; global results come from q Library and
project results come from the workspace index. `get_skill` stores the selected
`SKILL.md` or relative resource as a Loom artifact without copying its body
into the tool response.

Skills are discovered in increasing precedence order:

1. `~/.agents/skills/`
2. `~/.q/skills/`
3. `<workspace>/.agents/skills/`
4. `<workspace>/.q/skills/`

The later definition wins when names collide. The `/skills` manager keeps
global and current-session (workspace/project) entries in separate panels,
including shadowed paths, and reports validation failures. A skill name must
match its directory. Global and project skills are retrievable by the main
agent, Griller, and Scout. Planner, Coder, and the isolated commit agent do not
receive the retrieval tools.

Open `/skills` to manage Git-backed skills without leaving the screen:

```text
Tab / Left / Right   switch Global and Session panels
Up / Down            select a skill
A                    clone a Git repository into the focused scope
U                    pull the selected checkout with --ff-only
D                    confirm removal of the selected checkout
R                    rediscover skills and reconcile the Bleve index
```

Managed checkouts live under `~/.q/skills` or `<workspace>/.q/skills`.
Library/workspace startup, explicit reload, and managed Git operations
reconcile added, changed, and deleted checkout metadata. Unchanged digests are
not rewritten or reindexed, and `search_skills` performs queries only.
Portable `.agents/skills` entries are shown as externally managed and remain
read-only in this screen.

Skill resources are read-only, size-bounded, and confined to the resolved
skill directory. The experimental `allowed-tools` metadata is parsed but never
grants capabilities beyond tools already supplied by q. Arbitrary execution of
user-level skill scripts is not part of this initial support; project scripts
inside the workspace can still be invoked through the ordinary command tool
when the active agent already has that capability.

See [Agent Skills](docs/agent-skills.md) for indexing, precedence, Git
management, and capability-boundary details.

## Global propositions

q Library stores and reads durable global proposition records through its
authenticated API. A workspace-local checkpoint state machine accumulates
conversation events and closes a learning segment at 45% of the Thinker
model's context, after a successful `task_complete`, when a complete plan is
approved, or when `/learn` or the active chat's session-only MCP `learn` tool
explicitly requests a boundary. The standalone `q-mcp` server does not expose
`learn` because it does not own the active conversation cursor. Raw tool calls
and ordinary tool results are excluded; assistant prose, the complete validated
`task_complete` result, and the complete approved plan are retained.
`/learn off` disables collection, automatic boundaries, explicit boundaries,
and queued Thinker processing for the current workspace. Already queued
segments remain durable and resume after `/learn on`; messages produced while
learning is disabled are not collected.
Checkpoints advance only after `thinking_complete`, so a failed or interrupted
extraction retries the same durable segment. The Thinker calls a private
`register_proposition` tool once per proposition. q Library durably queues each
call in `~/.q/library/proposition-jobs.sqlite`, retrieves up to five existing
propositions with hybrid search and recency disabled, and starts a fresh
`librarian` model session to choose `create`, `merge`, or `discard`.
Registrations wait for that serialized decision. Completed job payloads expire
after seven days, while compact idempotency receipts remain so delayed retries
return the same result.

`search_propositions` searches canonical text and generated query variants
with a default `created_at` recency boost; `get_proposition` returns the
selected proposition with provenance and extraction metadata. These read tools
remain part of the existing `q-tools` MCP server. The Library storage/API layer
also supports content/query embedding batches, multi-projection HNSW indexing,
record-level result collapse, hybrid reciprocal-rank fusion, and atomic
proposition deletion. With a global embedding model configured, Thinker
registration automatically embeds the canonical proposition and generated
queries as one batch, while MCP proposition search automatically embeds its
query. Without that configuration, retrieval remains BM25-only.

See [Global Library](docs/library.md) for service ownership, authentication,
leader election, storage, and proposition API details.

## Discovery and `.qignore`

Repository discovery always excludes q's root `.q/` directory and honors a
workspace-root `.qignore`. Rules support blank lines, `#` comments, `!`
negation, leading `/` root anchors, trailing `/` directory matches, and `*`,
`**`, and `?` wildcards.

```gitignore
# Gradle outputs and checked-in vendor sources
.gradle/
build/
vendor/
```

These rules affect discovery tools such as `list_directory` and therefore the
Loom artifacts derived from them. Explicit reads remain possible. Shell output
is not rewritten, so agents are separately instructed to honor `.qignore`
during command-based scans.

Use `/ignore` to edit the file. `Ctrl+S` saves immediately; `Esc` requires a
second press before discarding unsaved changes.

## Language servers

Use `/lsp` from chat or run `q lsp` to manage language servers without editing
configuration files directly. The `Global Servers` panel stores trusted server
profiles in `~/.q/config.yaml`: profile ID, supported languages, executable,
arguments, enabled state, and the default profile selected for each language.
The `Workspace Roots` panel stores workspace-relative project roots in
`.q/lsp.json`, including language, optional server override, discovery source,
and enabled state.

Use `Tab` to switch panels, `A`/`E`/`D` to add, edit, or remove an entry,
`Space` to enable or disable it, `M` to make a server the global default for
its supported languages, and `Ctrl+S` to save both scopes. Unsaved changes
require a second `Esc` to discard.

Press `R` in the servers panel to detect known language-server executables on
`PATH`. Current candidates are `gopls`, `rust-analyzer`,
`typescript-language-server`, and (in preference order) `basedpyright`,
`pyright`, or `pylsp`. Detection adds portable command profiles to the draft;
it does not execute the binaries or persist anything by itself.

Press `R` in the roots panel to discover projects and the installed servers
needed by their languages. Project discovery currently detects Go workspaces
and modules, Rust workspaces and crates, TypeScript and JavaScript config roots,
and Python `pyproject.toml` roots. Language-level workspace markers fold nested
modules of the same language. Discovery skips q metadata, common
dependency/build directories, and paths excluded by the workspace `.qignore`.
All discovered entries remain draft-only until `Ctrl+S`.

The main TUI and `q-mcp` consume these settings at runtime. q lazily creates one
session per enabled `(project root, language)` pair, initializes the server on
the first routed query, and synchronizes the current on-disk file with
`didOpen`/full-content `didChange`. File-scoped calls choose the deepest
matching root; workspace-symbol searches without a path fan out across enabled
roots and return bounded, deduplicated results.

The MCP surface is read-only: `lsp_status`, `lsp_diagnostics`, `lsp_hover`,
`lsp_definition`, `lsp_references`, `lsp_document_symbols`, and
`lsp_workspace_symbols`. It is available to the main agent, Scout, Planner
review, and Coder, while Griller and the isolated commit agent do not receive
it. q rejects `workspace/applyEdit` and does not expose rename, formatting,
code-action execution, or arbitrary language-server commands.

Diagnostics distinguish `clean`, `issues`, and `unavailable`; the default wait
for a newer `publishDiagnostics` notification is one second. Saved LSP changes
apply when the tool runtime is next rebuilt, not by mutating already-running
sessions. See [LSP integration](docs/lsp.md) for routing, position encoding,
synchronization, and failure behavior.

## Loom

Every non-Loom MCP result is captured as an immutable artifact under `.q/loom`.
The model receives a bounded receipt containing a `loom_ref`, metadata, and
either a small complete result or a preview. Large structured output therefore
remains queryable without being copied through every subsequent prompt.

Artifacts are content-addressed and deduplicated. Defaults are 64 MiB per
artifact and 256 MiB per workspace store. `/loom` displays artifact, blob, and
byte usage and edits these limits and the automatic-GC policy.

Automatic GC starts at the configured trigger ratio and aims for the target
ratio. References from every persisted session and plan checkpoint, including
their parent lineage, remain live. Durable archive records do not pin Loom
artifacts. New artifacts remain protected for the configured grace period.
Setting `loom.gc.disabled: true` disables automatic collection but leaves manual
preview and collection available.

The model can use `loom_inspect`, `loom_read`, and `loom_eval`. `loom_eval` runs
JavaScript in a short-lived child process with access only to `inputs` and the
bounded `loom.inspect`, `loom.read`, `loom.get`, and `loom.json` APIs. It has no
filesystem, process, module, environment, or network API. Its JSON result is
stored as a new artifact with parent references and a script digest.

## Durable history and context

The current transcript and request context are persisted separately. When model
context metadata is known, q compacts request context at 85% to at most 22%
while leaving the complete transcript visible. Gateway metadata takes priority;
`provider.context_window` caches it, and `context.window` is only a fallback.

q retains the Gateway's `conversation_id` across user turns and tool rounds so
the selected provider can reuse its prompt cache. If an upstream returns its
own non-empty conversation ID, that provider-native value takes precedence over
the Gateway's generated cache-affinity ID. Conversation IDs are scoped to the
current q process and are never written to the workspace session, so a restart
always begins a new conversation and cache lifecycle. They are also cleared on
`/clear`, model/provider changes, and successful context compaction, where the
request prefix begins a new lifecycle.

After a completed chat response, the footer reports prompt-cache accounting
when the provider supplies `prompt_tokens_details.cached_tokens`, for example
`Prompt cache 82% · 49.2k/60.0k`. The numerator is cached prompt tokens and the
denominator is the complete prompt, including both cached and newly processed
tokens. When only the total is available, q instead shows
`Prompt cache not reported · prompt 60.0k`; this means the upstream omitted
cache accounting, not necessarily that it missed the cache.

Each isolated subagent execution has its own conversation lifecycle. Scout,
Griller, Planner, Coder, and Planner review reuse the ID returned by their first
model round for later tool/model rounds, then discard it when that execution
ends. They never share the main chat ID or another subagent's ID. If a model
group falls back to another candidate, the fallback request starts without the
previous candidate's ID and retains the replacement candidate's returned ID.

Chat messages, tool activity, subagent lifecycle events, questions, failures,
results, and compaction summaries are written through Workspace Memory to the
workspace's Session Store records. The model
can search this durable history with `search_archive` and page selected records
with `get_archive_record`. Text, metadata, time bounds, sorting, and recency
weighting are supported. With an embedding model configured, new history
records are embedded in the background writer, existing records are backfilled
in bounded batches, and relevance searches use BM25/HNSW reciprocal-rank
fusion. Filter-only or chronological searches do not call the embedding
provider. Without embedding configuration, archive retrieval remains BM25 and
metadata based. Embedding failures do not discard the underlying history
record, so lexical retrieval remains available for records awaiting backfill.

When a resumed Codex conversation returns the specific `no rollout found for
thread id` failure, q retries that request once on a fresh provider thread using
the complete message history supplied with the current request. Other provider
errors and retry failures are returned normally rather than being hidden by
recovery.

The Workspace Memory server is the only process that opens these JSON records,
Bleve, and HNSW while the workspace is leased. The JSON records remain the
source of truth; Bleve and HNSW are derived indexes and can be rebuilt from
those records. See [Workspace Memory](docs/workspace-memory.md) for service
ownership and [Session Store notes](docs/session-store-notes.md) for the storage
design.

## Development

```powershell
task test
go vet ./...
task build
task dist:check
```

The two command entry points are:

- `./cmd/q`: interactive chat plus the `acp`, `agents`, `commit`, `gateway`,
  `library`, `memory`, `model`, `mcp`, `skills`, `ignore`, `lsp`, and `help` command
  surfaces.
- `./cmd/q-mcp`: workspace MCP stdio server. It performs the one-way legacy
  session migration, leases archive records and indexes from Workspace Memory,
  opens Loom/LSP/Library integrations locally without a process-lifetime
  workspace lock, and excludes the chat-session-only `learn` tool.

Additional design and operational notes live in:

- [Subagent architecture](docs/subagent-architecture-notes.md)
- [Plan orchestration](docs/plan-orchestration.md) and
  [execution orchestration](docs/execution-orchestration.md)
- [Session Store](docs/session-store-notes.md)
- [Workspace Memory](docs/workspace-memory.md)
- [Agent Skills](docs/agent-skills.md)
- [LSP integration](docs/lsp.md)
- [Global Library](docs/library.md)
