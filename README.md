# q

`q` is an interactive multi-provider chat client built with Bubble Tea. It
embeds `snowmerak/llm-provider` and runs its OpenAI-compatible Gateway as a
supervised child of the current `q` executable.

```powershell
go run ./cmd/q
```

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
context:
  trigger_ratio: 0.85
  target_ratio: 0.22
  recent_ratio: 0.07
```

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
      "base_url": "http://localhost:1234/v1"
    }
  ]
}
```

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
- `/model`: load the current provider's models and change the active model
- `/provider`: list, add, edit, enable, disable, or delete Gateway providers
- `Ctrl+L`: clear the in-memory conversation
- `Ctrl+S`: send (alternative shortcut)
- `Ctrl+P`: edit provider settings (alternative shortcut)
- `Esc` or `Ctrl+C`: quit

The full chat transcript and separate request context are persisted in the
current workspace. When model context metadata is available, q
automatically compacts that request context at 85% to a maximum of 22% while
keeping the full transcript visible. Set `context.window` when the provider does
not expose `context_length`; discovered model context metadata is otherwise saved
as `provider.context_window`.

Each chat request exposes q's workspace-scoped builtin MCP tools to the model.
When the model returns tool calls, q executes them, appends their results to the
conversation, and continues the same turn automatically. Filesystem tools are
root-jailed; shell commands start in the workspace but are not an OS sandbox.
