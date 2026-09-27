# System One HTTP client

This package calls the native Jev-style System One API. It is separate from Q's
OpenAI-compatible chat client because System One returns structured decisions,
not generated chat text.

```go
c, err := systemone.New(systemone.Config{
    APIKey: os.Getenv("SYSTEM_ONE_API_KEY"),
    // BaseURL defaults to https://system-one.dev/v1.
})
if err != nil {
    return err
}

result, err := c.Evaluate(ctx, systemone.Request{
    Model: "jev-latest",
    State: "I was charged twice for one order.",
    Questions: map[string]systemone.Question{
        "team": {
            Type: systemone.QuestionChoice,
            Instructions: "Choose the reviewing team.",
            Criteria: map[string]string{
                "billing": "Payments and refunds",
                "support": "Technical help",
            },
        },
        "urgent": {
            Type: systemone.QuestionNoul,
            Instructions: "Does this require urgent attention?",
        },
    },
}, systemone.CallOptions{IdempotencyKey: operationKey})
if err != nil {
    return err
}
_ = result.Answers["team"].Choice
_ = result.Header.Get("X-Request-Id")
```

`EvaluateRaw` sends an existing JSON object without changing its bytes. Use it
for exact numeric literals, native extension fields, or explicit JSON nulls.
`ListModels` reads the native `{ "models": [...] }` catalog. `Result.Raw` and
`Answer.Raw` retain response extensions; response headers carry platform IDs,
credits, and rate limits. HTTP failures return `*APIError` with status,
classification, retry timing, and raw body. Error strings exclude body text.

The client does not retry a decision automatically. Keep the same request and
idempotency key when recovering an uncertain outcome. A missing key makes each
call a separate operation. Custom System One hosts, including Jev-compatible
services, can be selected with `Config.BaseURL` and an explicit API key.

Wire behavior follows the [System One API reference](https://docs.system-one.dev/en/docs/api)
and its [idempotency rules](https://docs.system-one.dev/en/docs/api/idempotency).
