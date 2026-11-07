# Browser control

Settings → Browser control records which CLI the user prefers for browser work.
The presets are CDP Q Driver, Ego Browser and Playwright CLI. The preference is
disabled by default. Enabling it adds guidance on the next user turn; changing
or disabling it updates existing conversations, including persistent Codex
threads, without resetting their identity. ACP sessions receive the same
guidance. Native workers receive it when their tool catalog allows commands.
ACP checkpoints retain the last delivered preference so reconnecting can apply
changes or clear a preference disabled while the session was closed.

Q stores the executable and argument array separately in global `config.yaml`:

```yaml
browser_control:
  enabled: true
  preferred: cdp-q-driver
  drivers:
    cdp-q-driver:
      args: [cdp]
    ego-browser:
      command: ego-browser
    playwright-cli:
      command: playwright-cli
```

An empty CDP executable selects the current Q binary with `cdp` as its first
argument. A bare executable name uses PATH; a full path selects that file.
Do not add shell quotes or arguments to the executable field. Arguments use a
JSON array in Studio, such as `["--profile", "with spaces"]`. The status check
only resolves paths and does not start a browser or install another CLI.

The prompt identifies the selected CLI and asks the agent to discover relevant
skills itself. No skill binding is stored. A CLI without a skill remains usable
through its help command. This setting grants no tools or additional permissions.

## Built-in CDP CLI

`q cdp` uses the pinned public `cdp-q-driver/cli` entrypoint, including the `cdp`
prefix when re-executing a background control process. Its flags, session state
and resource ownership follow the upstream CLI. A Chromium browser is needed
for browser operations. Node.js is needed only for `q cdp run`.

```sh
q cdp help
q cdp session list
q cdp --session research page new --name main https://example.com
```

## Bundled skill updates

Q carries the `q-cdp` skill and its reference resources in the binary. Library
installs them under `<config>/library/builtin-skills/<digest>/q-cdp` and atomically
selects a generation with `current.json`. This source is separate from user
skills, uses ordinary skill search/read APIs, and keeps its skill ID stable
across updates. User skills with the same name take precedence. Git skill
update/removal operations do not modify bundled resources.

Startup compares a bundle version (pinned driver timestamp plus resource revision)
and a digest of every resource. Unchanged files are reused. New binaries also
send their bundle when joining an already running Library with bundled-skill
support. Matching versions and hashes in its health response skip the upload.
The server reloads discovery and reconciles indexes after an update;
older Q processes cannot downgrade a newer installed bundle. Old immutable
generations are retained so in-flight resource reads can finish.

Library versions predating this feature keep serving their existing skills;
restart that Library once to enable bundled-skill support. Replacing a binary
does not change an already running process until it starts again.

When updating the pinned driver dependency, run
`go generate ./internal/bundledskills`, update the bundle version in
`internal/bundledskills/bundle.go`, and update its pinned-version test. A resource
edit without an upstream change increments the four-digit resource revision.
Generation reads the pinned Go module rather than fetching upstream HEAD.
