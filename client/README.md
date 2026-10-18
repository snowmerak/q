# OpenAI-compatible client

This package wraps `github.com/snowmerak/llm-provider/providers/openai` instead
of implementing a second HTTP stack. It supports chat completions, streaming,
tool calls, model listing, embeddings, and raw Responses API requests.

Function tools with no explicit strictness use `strict: false` in Responses
mode, matching Chat Completions' default and preserving optional parameters.
Explicit strictness is preserved. OpenAI strict mode is a narrower schema
contract: every object must reject additional properties and require all of its
properties, with optional values represented as nullable. A schema with optional
properties or free-form maps must not advertise `strict: true`. Q still validates
tool arguments locally before execution; provider strictness does not replace
that validation. Raw Responses requests retain the caller's supplied fields.

```go
c, err := client.New(client.Config{
    BaseURL:      "http://127.0.0.1:8080/v1",
    APIKey:      "optional-local-key",
    DefaultModel: "local/model",
})
if err != nil {
    return err
}
defer c.Close()

response, err := c.Chat(ctx, client.ChatRequest{
    Messages: []client.Message{{
        Role:    client.RoleUser,
        Content: "Hello",
    }},
})
```

When `BaseURL` and `APIKey` are empty, the underlying provider uses
`OPENAI_BASE_URL`, `OPENAI_API_KEY`, and its standard endpoint defaults. Set
`DisableAPIKey` for a local server that must not receive an environment key.

When this client targets a trusted Q Gateway, `ForwardUsageMetadata` sends the
bounded role from `WithUsageRole` and a per-call event ID. The Gateway consumes
these headers for `q usage`, removes them before provider dispatch, and uses the
event ID to deduplicate its record from an optional client-side
`UsageRecorder`. Leave this option disabled for direct provider endpoints.

`Config.Retry` opts into bounded retries for connection failures, truncated
responses, HTTP 408/429 and HTTP 5xx. Each physical attempt gets its own usage
event ID. Chat and native Responses streams can reconnect only before returning
their first chunk/event; partial output and tool fragments are never replayed.
Cancellation and deadlines stop both requests and backoff waits. Direct clients
default to one attempt. Q's managed clients default to four attempts and resolve
the current Gateway address and credential again on every attempt.
