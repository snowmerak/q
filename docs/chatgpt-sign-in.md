# ChatGPT plan connection in Q Studio

In local Q Studio, open **Settings → Providers**, add or edit a provider, and
select **ChatGPT plan** as its API type. Enable it and select **Continue with
ChatGPT**. Complete consent in the OpenAI browser tab, then return to Studio;
the connection polls while sign-in is pending. Use **Settings → Models** to
choose a model available to the selected account. ChatGPT models use Q's native
Responses mode and its existing agent loop and local tools.

Account status and actions wait until provider ID, API type and enabled state
have been saved to the running Gateway. Editing those fields temporarily disables
account actions; successful saves automatically refresh the connection.

The account selector controls the ChatGPT account used by Q, shared across all
ChatGPT provider aliases. **Reconnect** reuses the existing account registration.
**Add another account** is the explicit action for a separate account/workspace.
**Disconnect** revokes the active renewable session; it preserves registration
metadata so reconnecting does not create another app. Finish sign-in before
changing Gateway settings; Q rejects replacement of the callback's child process
while sign-in is pending. Process termination or OS failures can still interrupt
first registration before the server has returned its issued client ID.

Q uses the actual app identity `q` / `Q`, and stores credentials under
`<Q configuration directory>/chatgpt`. It overwrites imported ChatGPT identity
or directory settings at the process boundary so copied provider configuration
cannot copy a host identity. All Gateway generations use this same local store
and its OS lock. Provider IDs and model prefixes do not define OAuth identities.
Standalone llm-provider and Q are separate applications and can therefore appear
as two expected connections in ChatGPT. Q does not impersonate Codex.

Studio proxies only safe account-management results to the authenticated local
Gateway child. OAuth credentials, client IDs, subjects and installation IDs never
reach the browser or `providers.json`, and ChatGPT runtime options are omitted
from settings exports. Account actions require loopback access, a local Host,
and same-origin requests. Windows uses user-scoped DPAPI; Unix uses owner-only
files and directories and does not encrypt the account store at rest. Keep that
store local; use separate authorization on another host.

Inference uses the public Responses endpoint with full stateless input replay,
`store:false` and `stream:true`. A non-streaming Q caller receives the completed
stream's terminal JSON. Q does not silently fall back to a paid API key or another
account. Unsupported plan options, including output-token caps and temperature,
produce errors; do not enable those overrides for ChatGPT models. Hosted image
generation, file search, Code Interpreter, hosted MCP and tool_search are outside
this preview's contract. Q's client-side function tools use supported namespaces.
The generated `functions` namespace includes its own required description,
separately from the descriptions on individual tools. Omitting that namespace
description causes HTTP 400 (`Missing required parameter: 'tools[0].description'`)
even when every child tool has a description; reconnecting cannot repair it.

Studio recognizes ChatGPT as a streaming provider, including in model groups.
Both caller modes must collect final `response.output_item.done` snapshots before
returning the completed response. ChatGPT may leave the terminal event's output
array empty or partial; using only that event loses the answer and causes
`Responses completed without text or function calls` even after successful
inference. Collection preserves text, function calls, opaque reasoning and usage.

Completed refusal content is shown as an assistant answer, with its native
output preserved for subsequent requests. Streaming refusal deltas are displayed
once, and failed/incomplete streams retain the server's error code, explanation
and incomplete reason. Chat Completions' `stream_options.include_usage` is kept
off the upstream Responses request, whose completion event already includes usage.

ChatGPT cache affinity is automatic, without a Studio setting. Q's default native
Responses route keeps its existing per-session cache ID across turns and saved-session
reloads. The Gateway maps that ID to `prompt_cache_key`, and the ChatGPT adapter
uses the same value for the upstream `session-id` header. Chat Completions callers
also reuse the returned conversation ID for subsequent turns. The header is added
only by the ChatGPT adapter, not globally forwarded
to other providers. This is `session-id`, not `X-Session-Id`.

Q preserves optional tool arguments with explicit `strict: false` unless the
tool declares a compatible strict schema. Strict mode requires every object
property to be required and `additionalProperties: false` on every object.
Declaring strict mode on schemas with optional fields causes HTTP 400, such as
the `external_search` error for missing `completion_criteria`. Local argument
validation still checks required values, types and unknown fields before tools
run.

## Local development and release validation

Q's `go.mod` pins llm-provider `v0.0.0-20261014004000-2a04b3c58860`, including
the ChatGPT namespace description and non-streaming collection fixes. The
additional Chat Completions streaming, refusal, error-detail and cache-session fixes in the
sibling checkout require publishing that provider revision and updating this
pin before release; candidate validation alone does not update the dependency.
The workspace contains Q and the existing
ACP modules; it does not need the sibling llm-provider checkout. Local
`go test ./...`, `go vet ./...`, and Go builds use
the published provider. Build the checked-in Studio assets with:

```powershell
cd studio/frontend
npm run check
npm run build
npm run test:e2e -- chatgpt.spec.mjs
```

The default `go run ./scripts/modulecheck` checks the published dependency pin.
Run it before releasing Q. For future changes that need review of both
unpublished checkouts together, use:

```powershell
go run ./scripts/modulecheck -llm-provider ../llm-provider
```

This builds disposable module archives and performs versioned installs of `q`
and `q-mcp` with `GOWORK=off`. It does not publish anything, alter the actual
dependency pin, or replace published module bytes in the shared module cache.
Passing that optional check verifies the candidate combination. Publish
llm-provider first, update Q's dependency pin and run the default modulecheck
before releasing dependent changes in Q.

Provider/OIDC tests use signed test tokens and mocked endpoints. The Studio
regression test verifies initial status through the real local Gateway child,
then exercises account selection, disconnect, credential-field absence and
reload with mocked safe status responses. Delayed-save regressions cover provider
renaming, conversion to ChatGPT and enabling a provider, including automatic
status refresh after the Gateway applies each change. The default tests do not
authorize another real app or spend ChatGPT usage. The earlier real OAuth/inference probe
revoked its session; this implementation still needs a fresh end-to-end consent
test before release.

For the tool-schema regression, explicitly set `Q_TEST_CHATGPT_TOOLS=1` and run
`go test ./tools -run '^TestLiveChatGPTAcceptsQToolCatalog$' -count=1 -v`.
This uses an already connected Q account and checks non-streaming/streaming
native Responses and Chat Completions streaming inference on GPT-6.1 Sol, with
31 tools advertised and automatic tool
selection. The probe never executes a tool, creates a Studio session or starts
a login. It spends ChatGPT usage.

Add `Q_TEST_CHATGPT_CACHE=1` to that opt-in run to check three growing conversation
turns in each API mode. This exercises Q's automatic cache ID through the real
Gateway and ChatGPT adapter without manually supplying a key or header. It checks
that affinity stays stable and that a subsequent turn reports cached tokens.

For the Studio path, set `Q_TEST_CHATGPT_STUDIO=1` and run
`go test ./studio -run '^TestLiveStudioChatGPTPersonalChat$' -count=1 -v`.
This uses isolated Studio services, creates a disposable personal chat with
failed user-only history, and checks two real turns and their saved answers.
Learning is disabled for this test chat. Both existing-login checks completed
successfully on 2026-10-14; they do not verify the consent flow above.

Contract and lifecycle details live in the sibling
`llm-provider/providers/chatgpt/README.md` and the official
[Sign in with ChatGPT documentation](https://developers.openai.com/siwc/token-sharing-open-source/sign-in).

Validation used Go 1.27.1 on Windows ARM64. Both repositories' full Go suites,
builds and vet checks passed; the final affected-package checks also passed.
Studio's type check, production asset build, account-action browser regression,
and desktop/mobile inspection passed. Both the candidate module installation
and the default installation check using the published llm-provider pin passed.
The ChatGPT provider and changed lines pass golangci-lint 2.14.0 with errcheck,
govet, ineffassign, staticcheck and unused enabled; existing unrelated findings
remain in broader package lint runs. Race testing is unavailable on Windows ARM64
and govulncheck was not installed, so those checks were not run.
