# OpenAI-compatible client

This package wraps `github.com/snowmerak/llm-provider/providers/openai` instead
of implementing a second HTTP stack. It supports chat completions, streaming,
tool calls, model listing, embeddings, and raw Responses API requests.

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
