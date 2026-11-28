# Sign in with ChatGPT

`chatgpt.Provider` owns the public Sign in with ChatGPT flow and calls
`https://api.openai.com/v1/responses`. It supports native Responses and adapted
Chat requests, including streaming, local function tools, and stateless native
output replay. It does not read Codex credentials.

```go
p, err := chatgpt.New(chatgpt.Options{
    AppID: "my-app", AppName: "My App", Directory: localAccountDirectory,
})
// Handle err and close p when the owning app exits.
authorizationURL, err := p.StartLogin(ctx, false)
// Let the user open authorizationURL and complete consent in their browser.
status, err := p.Status(ctx)
```

Use stable app identity and a local directory across restarts. The default app
name is `llm-provider`, with state under the OS user configuration directory at
`llm-provider/chatgpt` (Local AppData on Windows to avoid roaming identity).
Hosts embedding the library should use their own actual
application name. Provider aliases in one app should share that app's store.

The Gateway configuration is:

```json
{"id":"chatgpt","type":"chatgpt","prefix":"chatgpt","enabled":true}
```

An optional `chatgpt` object accepts `app_id`, `app_name`, and `directory`.
`base_url`, `api_key`, and `api_key_env` are rejected for this type. Tokens and
installation host IDs are never configuration fields.

Account management is restricted to loopback connections with a local Host and
same-origin browser requests. Use `GET /v1/providers/chatgpt/chatgpt` for status,
and JSON POSTs to the following suffixes:

| Action | JSON body | Result |
| --- | --- | --- |
| `/login` | `{"new_account":false}` | `authorization_url` for normal sign-in or reauthorization |
| `/login` | `{"new_account":true}` | Explicitly add a separate account registration |
| `/select` | `{"profile":"account_..."}` | Select a verified local account |
| `/disconnect` | `{}` | Revoke the active renewable session and clear its tokens |

Status returns opaque local profile IDs, account labels, permission state,
pending state and safe errors. It never returns OAuth tokens, subjects, issued
client IDs or installation IDs. Failed login attempts remain unverified and
cannot be selected. A login is pending for at most ten minutes; closing the
provider cancels its listener. A standalone Gateway config reload keeps the
retired callback owner alive until login finishes while the replacement serves
requests. Finish browser sign-in before terminating or restarting the app.

## Registration and credentials

- Generate one installation host UUID, then preserve it with issued client IDs.
- Save the callback's issued client ID before token exchange and validation.
  Retry that registration after failures; disconnect preserves it as well.
- Use fresh state, nonce and S256 PKCE for each login. Validate OIDC signature,
  issuer, audience, expiry, nonce and subject with `go-oidc`. Reauthorization
  cannot change the bound subject; use explicit Add account instead.
- Attempt revocation of a newly minted renewable session if identity validation
  or persistence fails. If revocation is not confirmed, the safe status error
  tells the user to disconnect the app in ChatGPT settings. Do not automatically
  sign in, retry registration, switch accounts,
  refresh after terminal credential errors, or fall back to API-key billing.
- Serialize refresh, selection and revocation using an OS file lock across
  processes; save the rotated refresh token atomically before returning access.
  Transient failures retain credentials. Unusable refresh grants clear tokens
  and require reauthorization using the saved registration.
- Honor `earliest_refresh_at`. Do not blindly retry a rejected inference call.

Windows stores the account file using user-scoped DPAPI encryption. Unix stores
use a directory mode of 0700 and a file mode of 0600; Unix storage is not encrypted
at rest. Keep the credential directory local and out of version control and
settings exports. Copying or deleting it can change host attribution or force
new registration. DPAPI does not make credentials a portable export format.

The provider fixes the OAuth and resource endpoints and disables HTTP redirects.
OAuth tokens stay in the owning Go process and local store; browser URLs contain
only the public authorization parameters. Request headers cannot replace the
owning application's originator or User-Agent.

## Inference contract and validation

Each request uses `store:false`, `stream:true`, a full input array, and opaque
reasoning replay. System messages become developer messages. Flat local tools
are grouped under the `functions` namespace with its required description,
separate from each child tool's description. Native namespaces are supported;
mixing flat tools with an existing `functions` namespace is rejected.

Unsupported plan fields (including output-token caps, temperature, top-p and
HTTP `previous_response_id`) and hosted tools fail explicitly. Non-streaming
Responses and both Chat caller modes combine the terminal response with the final
`response.output_item.done` snapshots. The terminal event can omit or truncate
its output array; collection preserves the streamed text, function calls and
opaque reasoning in output-index order without duplicating terminal items.
Refusal text and deltas remain visible assistant content, while native refusal
items are preserved for replay. Failed and incomplete streams retain the server's
error code, message and incomplete reason, and subsequent reads return EOF.
The Chat-only `stream_options` field is not forwarded to Responses; completion
events already supply usage without `stream_options.include_usage`.
For ChatGPT routes, the Gateway uses conversation cache affinity as
`prompt_cache_key` in both API modes. The ChatGPT adapter defaults the upstream
`session-id` header to that key, preserving an explicit caller-supplied header.
There is no process-global session ID and no new ID is generated for each turn;
callers must retain their conversation affinity. Other providers do not receive
this header automatically.
EOF, failed/incomplete events,
and usage-limit failures after partial output never count as completed inference.
The selected account's public model catalog supplies model IDs and reasoning
capabilities; account switching refreshes Gateway choices.

`go test ./providers/chatgpt ./gateway` covers signed OIDC callbacks, failed
exchange/retry, host/client continuity, protected Windows storage, invalid
identity revocation, serialized refresh, transient/terminal renewal errors,
stateless replay, and incomplete streams. These are mocked endpoint tests, not
a new production OAuth session. The earlier real-account probe is recorded in
`experiments/siwc-probe/README.md`; it revoked its session and saved no tokens.

The public service is still a preview. Client-side safeguards cannot guarantee
that an undocumented server anomaly detector never flags a request. Q and a
standalone llm-provider app intentionally have distinct app registrations;
normal restarts and provider renames within either app preserve its identity.

Official contract:
[sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in),
[profiles and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions),
[models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference),
[preview limits](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).
