# Q fork delta

Q maintains this module as `github.com/snowmerak/q/third_party/acp-go-sdk`,
derived from `coder/acp-go-sdk` v0.13.5 under the original Apache-2.0 license.
See `UPSTREAM.json` for the upstream commit and protocol references. This is
a source fork in q, not a Git submodule: running `git pull` here updates q.

## Protocol changes

`schema/q-overrides.json` adds scope properties missing from v0.13.5's unstable
`CreateElicitationRequest`, following the
[ACP elicitation RFD](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/elicitation.mdx).
The generator applies the overlay to unchanged upstream schemas. Form and URL
variants both receive:

- `sessionId`: session scope, optionally with `toolCallId`.
- `requestId`: originating request outside a session, instead of `sessionId`.

Session/tool IDs are optional string aliases. RequestId is a pointer to the
JSON-RPC ID union: absence is omitted and a supplied numeric zero is retained.
As upstream, generated Validate methods are not full JSON Schema validators;
callers must choose one valid scope. Q still sends session-scoped forms and
negotiates form capability. This does not enable URL elicitation in Q.

The old hand-written Form.SessionId patch is now generated and omitted when
absent to permit request-scoped use. The unexplained omitempty change on
CancelNotification.SessionId was removed; cancellation follows the original
required-field schema.

## Maintenance changes

- Module/import paths, including examples and generator, use Q's fork path.
- `cmd/generate` was restored from upstream commit
  `0845a3bb9eddda5bfc22a94dd3598c90cb842451`. The original module zip excluded
  this nested module, losing the generation tool.
- The generator applies additive properties, rejects incompatible upstream
  definitions, formats output and offers a non-mutating `-check`.
- Non-object union variants no longer generate unreachable object-shaping code
  after return. Wire behavior is unchanged; SDK-wide go vet now checks cleanly.
- Scope regression tests cover form/URL JSON round trips, numeric/string request
  IDs, omitted fields, and the required cancellation session ID.
- Q pins a Go-generated pseudo-version of a published SDK commit. There is no
  SDK_VERSION file or mandatory release tag; schema/version remains the upstream
  protocol baseline, not the fork's Go module version.

See [RELEASING.md](RELEASING.md) and the
[Q maintenance guide](https://github.com/snowmerak/q/blob/main/docs/acp-go-sdk-patch.md).
