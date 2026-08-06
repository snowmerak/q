# q

`q` is an interactive multi-provider chat client built with Bubble Tea. It
embeds `snowmerak/llm-provider` and runs its OpenAI-compatible Gateway as a
supervised child of the current `q` executable.

```powershell
go run ./cmd/q
```

With [Task](https://taskfile.dev/) installed, the common commands are:

```powershell
task install  # install q and q-mcp into the Go binary directory
task build    # build both commands into ./bin
task run      # run q from source
```

The session store uses a pure-Go HNSW index for semantic search, so these tasks
do not require CGO, FAISS, or a vector-specific build tag.

On first launch, q asks for a provider ID, optional model prefix, selectable API
type, endpoint, and API-key source. API types are selected with `Left`/`Right`;
the prefix defaults to the provider ID when omitted. q then starts an internal
Gateway on a random loopback port, validates the provider through `/v1/models`,
and opens a searchable model selector. Personal configuration is stored at:

```text
~/.q/config.yaml
~/.q/providers.json
```

The selected model remains part of this global configuration. Chat state is
workspace-local: q resumes the transcript and compacted request context from
the directory where it is launched:

```text
<current-directory>/.q/session.json
```

The workspace session does not contain provider settings, API keys, or a model
override. `/clear` removes the local session as well as clearing the screen.

Example:

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
```

Subagent roles may override `model` and `reasoning_effort`. A missing role or
empty role model inherits `provider.model`; an omitted effort leaves the
provider's model default unchanged. The effort default is empty; empty or
whitespace-only values are omitted from model requests. Subagent execution resolves configured
roles against `/v1/models`. Explicit effort values are accepted only when the
selected model advertises `capabilities.reasoning.control: effort` and, when
enumerated, the value appears in `supported_efforts`. The built-in roles are
`griller`, `scout`, `research`, `planner`, `coder`, and `advisor`.

`providers.json` uses `llm-provider/gateway.Config` directly. Multiple enabled
providers are exposed together using their provider ID or configured prefix:

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
      "model_metadata": {
        "qwen3-32b": {
          "context_length": 131072
        }
      }
    }
  ]
}
```

Gateway `model_metadata` overrides upstream metadata before model IDs receive
their configured prefix. In the `/model` catalog, press `Ctrl+E` on a model to
set or clear its Gateway `context_length`; q replaces and validates the Gateway,
reloads `/v1/models`, and updates the active context cache immediately.

The child binds only to `127.0.0.1:0` and reports its assigned port to the
parent over a private stdout handshake. Provider changes start and probe a new
child before q saves the candidate configuration and replaces the old child.
Failed changes leave the running Gateway untouched.

On POSIX systems the directory is created with user-only permissions and both
configuration files are written with mode `0600`. Prefer an environment
variable over an inline key, especially on Windows where file modes do not
manage ACLs. Existing single-endpoint q configurations are migrated to a
`default` Gateway provider on first launch.

Chat keys:

- `Enter`: send
- `Shift+Enter`: insert a newline
- `/clear`: clear the conversation and remove the current workspace session
- `/model`: configure the main-loop, embedding, or subagent role models
- `/provider`: list, add, edit, enable, disable, or delete Gateway providers
- `Ctrl+L`: clear the in-memory conversation
- `Ctrl+S`: send (alternative shortcut)
- `Ctrl+P`: edit provider settings (alternative shortcut)
- `Esc` or `Ctrl+C`: quit

`/model` first shows `default` (the main chat loop), `embedding`, and then the
built-in subagent roles. The shared catalog shows each model's Gateway context
length and marks explicit Gateway overrides. Press `Ctrl+E` on any model to edit
that per-model override.

Selecting `embedding` asks for a vector dimension between 1 and 4096. Because
the common `/v1/models` response does not currently identify embedding-only
models, the picker shows the whole catalog. Press `i` on `embedding` to clear
the optional configuration.

Selecting a subagent role opens the same catalog; models that advertise
enumerated reasoning efforts add a final effort picker whose `default` choice
omits `reasoning_effort`. Select a subagent in the target list and press `i` to
remove that role's override and inherit the main-loop default again.

Assistant responses are rendered as terminal Markdown with headings, lists,
links, tables, block quotes, emphasis, and syntax-highlighted fenced code.
Rendering follows the detected light/dark terminal theme and current viewport
width. User input and tool output remain literal so their contents are never
reinterpreted as presentation markup.

The full chat transcript and separate request context are persisted in the
current workspace. When Gateway model context metadata is available, q
automatically compacts that request context at 85% to a maximum of 22% while
keeping the full transcript visible. Gateway metadata has priority and is
cached as `provider.context_window`; the file-only `context.window` setting is
used only when the Gateway does not expose `context_length`.

Each chat request exposes q's workspace-scoped builtin MCP tools to the model.
When the model returns tool calls, q executes them, appends their results to the
conversation, and continues the same turn automatically. Tool calls, command
status, exit codes, and command output are rendered as live progress while the
turn is running. Filesystem tools are root-jailed; shell commands start in the
workspace but are not an OS sandbox.
Every non-Loom MCP result is also captured as an immutable Loom artifact under
the workspace's `.q/loom` directory. The tool message contains a `loom_ref`,
artifact metadata, and either the complete small result or a bounded preview.
This keeps large structured results available without repeatedly carrying them
through model context. Artifacts are content-addressed, deduplicated, limited to
64 MiB each, and the workspace blob store is limited to 256 MiB.

The model can use `loom_inspect` for metadata, `loom_read` for byte ranges, and
`loom_eval` to transform one or more artifacts with JavaScript. `loom_eval`
exposes only `inputs` and `loom.inspect`, `loom.read`, `loom.get`, and
`loom.json`; it has no filesystem, process, module, environment, or network API.
Scripts run in a short-lived child process, must return a JSON value, and store
that value as a new artifact with parent references and a script digest. An MCP
result artifact is a JSON envelope whose `structured` field contains the
server's structured result and whose `content` field preserves its MCP content
parts.


The model can query durable workspace history with the read-only
`search_archive` and `get_archive_record` tools. Search supports text, record
metadata, RFC3339 creation-time bounds, sorting, and optional recency weighting;
it returns bounded excerpts. Full record content is paginated by
`get_archive_record`, while structured payload retrieval is explicit and size
bounded. Semantic retrieval remains inactive until the configured embedding
model is connected to record generation and backfill.
