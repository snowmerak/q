# q

`q` is a workspace-native terminal agent built with Bubble Tea. It combines a
multi-provider chat client, approval-gated planning and execution, isolated
subagents, durable workspace history, Loom artifacts, and an interactive Git
commit workflow.

`q` embeds [`snowmerak/llm-provider`](https://github.com/snowmerak/llm-provider)
and supervises its OpenAI-compatible Gateway as a child of the current `q`
executable.

## Quick start

Run from source:

```powershell
go run ./cmd/q
```

With [Task](https://taskfile.dev/) installed:

```powershell
task install  # install q and q-mcp into the Go binary directory
task build    # build both commands into ./bin
task run      # run q from source
```

Run `q` inside the directory you want to use as the workspace. The TUI renders
before Gateway, Session Store, Loom, and MCP initialization completes. Provider
model discovery runs in parallel with a 1.5-second budget; providers that do
not answer in time are skipped for that refresh instead of delaying the UI.

Run only the configured Gateway, without the TUI or workspace services:

```powershell
q gateway [--host <ip>] [--port <port>]
```

Run the global Library as a dedicated foreground service:

```powershell
q library
```

Configure the default Library bind host and fixed rendezvous port in the TUI:

```powershell
q library config
```

The same screen is available as `/library` from an ordinary q session. The
defaults are saved independently in `~/.q/library.json`; a running Library
leader must be restarted before listener changes take effect. Unlike the
Gateway's optional port `0`, the Library requires a stable port from `1` to
`65535` so other q processes can find the same service.

Ordinary `q` sessions also ensure that the same Library server is available.
When no compatible server is running, one session wins the user-level file
lock and embeds the server until that q process exits. Other sessions connect
over HTTP, and `q library` can be used when the service should remain available
independently of a workspace TUI.

The command reads providers from `~/.q/providers.json` and listener/API-key
settings from `~/.q/gateway.json`. The initial default is `127.0.0.1:0`, so the
OS selects an available port. A saved nonzero port is used when available and
falls back to port `0` on a collision; an explicit conflicting `--port` remains
an error. Command-line `--host` and `--port` values override the saved defaults.
The effective OpenAI-compatible `/v1` endpoint is printed after startup.

Standalone clients must send one of the managed API keys as an
`Authorization: Bearer` credential. Generate and revoke keys in `q gateway
config`; the plaintext is shown only once, while `gateway.json` stores a keyed
BLAKE3 digest. Key changes are reloaded by a running standalone Gateway.
Provider and listener changes require a restart.

Open the Gateway settings UI without initializing the main chat UI or workspace
services. Network defaults, API keys, and providers are separate sections:

```powershell
q gateway config
```

The remaining settings screens can also be opened directly. Each command loads
only the resources its screen needs instead of starting the main chat UI:

```powershell
q model   # managed Gateway + model discovery
q skills  # global and current-workspace Agent Skills
q ignore  # current workspace's .qignore editor
q lsp     # global language servers and current-workspace project roots
q help    # scrollable command and key reference
```

The Session Store uses a pure-Go HNSW index, so q does not require CGO, FAISS,
or a vector-specific build tag.

## First launch

On first launch, q asks for a provider ID, optional model prefix, API type,
endpoint, and API-key source. The prefix defaults to the provider ID. Provider
settings remain editable even when an upstream is offline.

Personal configuration is stored in:

```text
~/.q/config.yaml
~/.q/providers.json
~/.q/gateway.json
~/.q/gateway.key
~/.q/library.json
~/.q/library.key
```

Prefer environment variables over inline API keys. On POSIX, q creates the
configuration directory with user-only permissions and writes configuration files with
mode `0600`. Windows file modes do not manage ACLs.

## TUI reference

The regular chat footer stays compact. Use `Ctrl+H` or `/help` from any screen
to open the complete, scrollable guide. Closing help restores the previous
screen without discarding editor state or interrupting an active turn.

### Slash commands

| Command | Action |
|---|---|
| `/plan [request]` | Grill, research, approve, and execute a work plan. Without an inline request, the next message becomes the request. |
| `/commit` | Open the interactive commit workflow, then return to chat. |
| `/model` | Configure the main-loop, embedding, or subagent role models. |
| `/gateway` | Configure Gateway network defaults, API keys, and providers. |
| `/library` | Configure the global Library's default host and fixed port. |
| `/loom` | Inspect Loom usage and configure or run garbage collection. |
| `/ignore` | Edit workspace discovery rules in `.qignore`. |
| `/skills` | Interactively add, pull, remove, and reindex global/session Agent Skills. |
| `/lsp` | Configure global language servers and current-workspace project roots. |
| `/clear` | Clear the chat projection and remove the current workspace session. |
| `/help` | Open the command and shortcut guide. |

### Chat keys

| Key | Action |
|---|---|
| `Enter` / `Ctrl+S` | Send. |
| `Shift+Enter` | Insert a newline. |
| `Ctrl+P` | Open Gateway settings. |
| `Ctrl+H` | Open or close help. |
| `Ctrl+G` | Expand or collapse the detailed subagent trace when available. |
| `Ctrl+C` | Interrupt the active turn; quit while idle. |
| `Esc` | Leave the current screen or quit chat. |

Assistant responses render as terminal Markdown using the detected terminal
theme and current viewport width. User input and tool output remain literal.

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

Execution state is atomically checkpointed in `.q/plan-execution.json`. On the
next launch, interrupted work offers Resume, Inspect, and Discard. Resuming a
Coder that may already have changed files starts a new recovery attempt that
first inspects the workspace; a saved Coder result resumes at Planner review.
Discard removes the checkpoint but never reverts workspace changes.

See [plan orchestration](docs/plan-orchestration.md) and
[execution orchestration](docs/execution-orchestration.md) for the detailed
contracts.

## Commit workflow

Use `/commit` from the main TUI or run the standalone command inside a Git
worktree:

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

Static `models` remain available even when metadata discovery times out.
`model_metadata` overrides upstream metadata before prefixes are applied. In
the `/model` catalog, `Ctrl+E` sets or clears a model's Gateway
`context_length` override.

The Gateway binds only to `127.0.0.1:0` and reports the assigned port to its
parent over a private stdout handshake. Provider edits start a replacement
child before saving and activating it; a failed replacement leaves the running
Gateway untouched.

Each supervised Gateway generation also receives a newly generated temporary
API key. The parent `q` client uses that key automatically, and it is neither
written to `providers.json` nor retained in the child environment after
startup. Replacing the Gateway rotates both its endpoint and API key.

That supervised child credential is independent of the persistent keys used by
standalone `q gateway`: the parent process injects the temporary key into its
own clients automatically, so launching or replacing the child does not require
the user to copy a key.

`/model` lists the main `default` target, optional `embedding` target, and each
subagent role. A role without an explicit model inherits the main model. An
empty reasoning effort lets the provider choose its default; explicit efforts
are accepted only when advertised by that model. The built-in roles are
`griller`, `scout`, `research`, `planner`, `coder`, `commit`, `advisor`, and
`thinker`. The `thinker` role is reserved for durable proposition selection
and extraction; until that pipeline is connected, configuring it only records
the model and reasoning-effort policy.

The embedding picker accepts dimensions from 1 to 4096. Because the shared
model catalog does not identify embedding-only models, it currently shows all
models. Press `i` to clear an embedding or role override and restore inheritance.

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
agents:
  max_parallel: 3
  roles:
    planner:
      model: codex/gpt-5.6-sol
      reasoning_effort: high
    coder:
      model: codex/gpt-5.6-terra
      reasoning_effort: medium
    commit:
      model: codex/gpt-5.6-terra
      reasoning_effort: low
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
| `.q/session.json` | Current transcript and compacted request-context projection. |
| `.q/lsp.json` | Current workspace's language project roots and optional server overrides. |
| `.q/plan-execution.json` | Durable checkpoint for an approved plan execution. |
| `.q/data/records/` | Source records for durable workspace history. |
| `.q/index/bleve/` | Derived full-text index. |
| `.q/index/vectors.hnsw` | Derived semantic index when embeddings are available. |
| `.q/loom/` | Immutable, content-addressed tool artifacts. |
| `.q/workspace.lock` | Diagnostic metadata for the current or most recent lock owner. |
| `.qignore` | Workspace discovery exclusions. |

Only one writer may own a workspace. The interactive app, `q commit`, `q-mcp`,
and direct Session Store opens use an OS-backed exclusive lock for their full
lifetime. Another writer exits with owner diagnostics instead of opening or
rebuilding shared Bleve/HNSW state.

`workspace.lock` is not a sentinel. The OS lock lives on the open file handle;
after a crash or forced termination the handle closes automatically, while the
file remains as reusable diagnostic metadata.

`/clear` removes only the current chat projection. Durable archive records and
workspace file changes remain intact.

## Tools and task lifecycle

Each chat request exposes q's workspace-scoped builtin MCP tools. Developer
context describes the host OS, architecture, workspace root, and exact command
shell so generated commands use appropriate quoting. Filesystem tools are
root-jailed; shell commands start in the workspace but are not an OS sandbox.

The main agent also receives orchestration tools:

- `task_start` begins a tool-using or multi-step task.
- `task_complete` is required exactly once after a started task reaches a
  terminal outcome.
- `ask_to_user` pauses the current turn for a required decision. Choices return
  their stable ID; typing sends a free-form answer instead.

Tool calls, arguments, results, command status, and intermediate agent notes can
be inspected in the live detailed trace. Interrupting a turn cancels its request
context, records unfinished tool calls as cancelled, persists the cancellation,
and returns to the input prompt.

## Agent Skills

q supports the [`SKILL.md` Agent Skills format](https://agentskills.io/specification)
without injecting the complete skill catalog into model context. Global skill
metadata is projected into q Library, while project metadata is projected into
the workspace Session Store; both are indexed by Bleve. The main chat, Griller, and Scout can use
`search_skills`; global results come from q Library and project results come
from the workspace index. `get_skill` stores the selected `SKILL.md` or relative resource
as a Loom artifact without copying its body into the tool response.

Skills are discovered in increasing precedence order:

1. `~/.agents/skills/`
2. `~/.q/skills/`
3. `<workspace>/.agents/skills/`
4. `<workspace>/.q/skills/`

The later definition wins when names collide. The `/skills` manager keeps
global and current-session (workspace/project) entries in separate panels,
including shadowed paths, and reports validation failures. A skill name must
match its directory.
Global and project skills are retrievable by the main agent, Griller, and Scout. Planner,
Coder, and the isolated commit agent do not receive the retrieval tools.

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

## Global propositions

q Library can read durable global proposition records through the authenticated
Library API. `search_propositions` searches canonical text and generated query
variants with a default `created_at` recency boost; `get_proposition` returns
the selected proposition with provenance and extraction metadata. Both tools
are part of the existing `q-tools` MCP server. Proposition extraction, writes,
embeddings, and HNSW indexing are planned separately.

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

## Language server settings

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

These settings do not start language servers or expose LSP tools yet. Runtime
activation and MCP integration are a separate layer over the stored settings.

## Loom

Every non-Loom MCP result is captured as an immutable artifact under `.q/loom`.
The model receives a bounded receipt containing a `loom_ref`, metadata, and
either a small complete result or a preview. Large structured output therefore
remains queryable without being copied through every subsequent prompt.

Artifacts are content-addressed and deduplicated. Defaults are 64 MiB per
artifact and 256 MiB per workspace store. `/loom` displays artifact, blob, and
byte usage and edits these limits and the automatic-GC policy.

Automatic GC starts at the configured trigger ratio and aims for the target
ratio. References from the current session and active plan checkpoint, including
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

Chat messages, tool activity, subagent lifecycle events, questions, failures,
results, and compaction summaries are written to the Session Store. The model
can search this durable history with `search_archive` and page selected records
with `get_archive_record`. Text, metadata, time bounds, sorting, and recency
weighting are supported. Semantic retrieval becomes active when records have
embeddings; app-side record embedding and historical backfill are not yet
connected, so current archive queries remain text and metadata based.

The JSON records are the source of truth. Bleve and HNSW are derived indexes and
can be rebuilt from those records. See
[Session Store notes](docs/session-store-notes.md) for the storage design.

## Development

```powershell
go test ./...
go vet ./...
task build
```

The two command entry points are:

- `./cmd/q`: interactive chat and `q commit`
- `./cmd/q-mcp`: workspace MCP server
