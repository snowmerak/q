# q compatibility patch

This directory contains `github.com/coder/acp-go-sdk` v0.13.5 under its
Apache-2.0 license. q uses it through a local `replace` directive because the
released Go type for form elicitation omits the session/request scope present
in the current ACP v1 wire schema.

q adds one field to `UnstableCreateElicitationForm`:

```go
SessionId SessionId `json:"sessionId,omitempty"`
```

Remove this local replacement when the upstream Go SDK exposes a scoped form
elicitation request compatible with current ACP clients.
