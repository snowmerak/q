# Publishing Q's ACP SDK fork

This is a nested module in snowmerak/q, not coder/acp-go-sdk. Preserve the
original Apache-2.0 license and update UPSTREAM.json on upstream changes.
Upstream's GitHub release workflows and API secrets are not required.

## Versioning

Q pins a Go-generated pseudo-version of a published SDK commit. No tag or
SDK_VERSION file is required. The source checkout uses go.work to select the
local SDK; versioned go install ignores that workspace and uses q's go.mod.

- schema/version and version identify the upstream schema snapshot (0.13.5).
- schema/meta.json identifies the ACP wire protocol version (1).
- q's go.mod/go.sum identify the published fork revision and its checksums.

Do not manually construct pseudo-version timestamps or hashes. Go obtains
them from the published commit. If tags are introduced later, an SDK tag must
be prefixed with third_party/acp-go-sdk/; root v* tags belong to q.

## Publish in order

1. Commit the SDK changes and push the commit to q's main branch. SDK consumers
   can then resolve the fork's module path and source from that commit.
2. From q's root, remove a stale/unpublished SDK requirement if present, then
   let Go resolve the published module and populate go.mod/go.sum:

   ```sh
   go mod edit -droprequire=github.com/snowmerak/q/third_party/acp-go-sdk
   GOWORK=off go mod tidy
   ```

   These are POSIX commands; in PowerShell set `$env:GOWORK = 'off'` before tidy.
   If the requirement is already valid and only needs updating, use
   `go get github.com/snowmerak/q/third_party/acp-go-sdk@latest` instead.
   Proxies can cache latest queries; when diagnosing propagation, resolve the
   published main branch directly and let Go produce the canonical version.
3. Verify the resulting requirement is a published revision, not a local
   replacement, and commit/push q's updated dependency and checksums.
4. From outside the checkout with a temporary GOBIN, test versioned installation
   of both q binaries at the published q commit and at @latest.
5. Restore workspace mode for development.

The SDK commit precedes the q dependency commit: a commit cannot embed its own
future hash. During a bootstrap, publish the SDK subtree first, then the q
integration. All commits/pushes require repository-owner authorization.

## Checks

From q's repository root:

```sh
go -C third_party/acp-go-sdk/cmd/generate run . -check
go -C third_party/acp-go-sdk/cmd/generate test ./...
go -C third_party/acp-go-sdk test ./...
go test ./...
go run ./scripts/modulecheck
```

modulecheck installs temporary q/SDK snapshot archives with GOWORK=off.
It rewrites the SDK requirement/checksums only inside the test archive, never
in the checkout, and uses an isolated GOMODCACHE/GOBIN. It publishes nothing.
This checks packaging, not GitHub visibility or live Zed interoperability.

## Updating upstream

See the [Q maintenance guide](https://github.com/snowmerak/q/blob/main/docs/acp-go-sdk-patch.md).
`make update-schema VERSION=...` refreshes schema snapshots and regenerates.
`make generate` regenerates pinned snapshots plus Q's overlay without fetching.
`make release` only checks the SDK and prints the commit-based publication steps.
