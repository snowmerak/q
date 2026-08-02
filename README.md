# q

`q` is an interactive OpenAI-compatible chat client built with Bubble Tea.

```powershell
go run ./cmd/q
```

On first launch, q asks for the provider base URL, API-key environment variable,
and an optional inline API key. It then loads `/models` from that endpoint and
opens a searchable model selector. Personal configuration is stored at:

```text
~/.q/config.yaml
```

Example:

```yaml
version: 1
provider:
  type: openai-compatible
  base_url: https://api.openai.com/v1
  model: your-model
  api_key_env: OPENAI_API_KEY
  system_prompt: You are a helpful assistant.
```

On POSIX systems the directory is created with user-only permissions and the
configuration is written with mode `0600`. Prefer an environment variable over
an inline key, especially on Windows where file modes do not manage ACLs.

Chat keys:

- `Enter`: send
- `Shift+Enter`: insert a newline
- `/model`: load the current provider's models and change the active model
- `/provider`: edit provider settings
- `Ctrl+L`: clear the in-memory conversation
- `Ctrl+S`: send (alternative shortcut)
- `Ctrl+P`: edit provider settings (alternative shortcut)
- `Esc` or `Ctrl+C`: quit

The current chat history is held in memory and sent with every request, which
provides basic multi-turn conversation. Conversation persistence and agent tool
execution will be added in later stages.
