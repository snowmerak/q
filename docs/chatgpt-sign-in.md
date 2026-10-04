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

## Local development and release validation

Q's `go.mod` pins llm-provider `v0.0.0-20261004155157-5061a2c9fe38`, the
published commit with ChatGPT support. The workspace contains Q and the existing
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
status refresh after the Gateway applies each change. The tests do not authorize
another real app or spend ChatGPT usage. The earlier real OAuth/inference probe
revoked its session; this implementation still needs a fresh end-to-end consent
test before release.

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
