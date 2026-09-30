# Regression and integration tests

## Default suite

Run `task studio:install` once, then `task test`. The default suite includes the
root Go module, both nested ACP modules, generated ACP binding checks, and the
Studio Svelte/TypeScript check. Browser tests are opt-in and are not silently
counted as passing when Chromium is missing.

Focused integration and regression checks:

```powershell
go test ./studio ./gitwork ./tools/builtin -count=1 -timeout=3m
```

New Studio runtime integration tests use a real SessionHost, managed Gateway
child, Loom, filesystem tools, and session/event storage. Only the model is a
local OpenAI-compatible HTTP fixture. They cover:

- Guidance sent through the HTTP command API, interruption, a successor run,
  redirect events, latest-run lookup, actual file creation, and persisted
  guidance/tool results.
- Question answers, stale call IDs, pause/resume, and rejection after completion.
- Recovery from an active run with a stale snapshot and a partial final log
  record; a second restart must still replay all durable events exactly once.
- Complete JSON records missing a final newline, corrupt complete records, and
  cursor gaps. Only incomplete final JSON can be discarded during recovery.
- Gateway and System One provider creation/deletion, duplicate rejection,
  handler reopen, last-provider protection, and preservation of saved settings
  when Gateway application fails.

Git regressions use real temporary repositories and worktrees: an unchanged
task closes and removes its lease; parent changes, branch switches, and dirty
parents reject a merge without changing either checkout.

Command regressions run the platform shell: a wait timeout keeps the command
alive, a later wait observes completion, runtime close terminates a running
command, and output cursors report eviction and drain without duplicate bytes.

## Studio browser suite

```powershell
task studio:install
npm --prefix ./studio/frontend run test:e2e:install
task studio:test
```

`npm --prefix ./studio/frontend run test:e2e` is equivalent. Playwright is pinned
in the frontend lockfile; the install command installs its matching Chromium.
There are no retries. A missing browser fails with an installation diagnostic.

The suite builds the production frontend, then runs a Go test fixture on a
random loopback port. That fixture starts an isolated real Studio handler and
SessionHost with temporary settings, projects, workspaces, and local model
responses. It does not contact an external model or use your user settings or
provider credentials. The fixture shutdown endpoint allows Go to close its
services and remove temporary data before Playwright stops the process.

Browser assertions cover project/workspace selection and reset, a guided turn
without page refresh, replay after reload, repeated tool IDs in later turns,
Markdown headings and highlighted code, automatic checkbox persistence, and
project creation/edit cancellation/update/deletion. The folder browser starts
from home, closes before its owning dialog on Escape, and passes the selected
workspace to real session registration. Reopening a dialog resets its inputs.
Switching sessions aborts a held event request and rejects its late content;
a delayed session detail response cannot overwrite a newer selection. Input is
disabled until that selection finishes loading.
Integration browser tests exercise MCP transport, environment grants, role
grants, invalid JSON recovery, and deletion; queued LSP writes stay in their
original repository while the selected path changes. Ignore edits keep their
document's revision and flush on panel/route changes. A disposable portable
skill renders read-only and reindexes through the real local services.
Subagent tests cover profile role/prompt/tool/delegation grants and deletion,
queued revision chains and scope moves during a repository switch, and ACP
connection secret masking, enabled options, role bindings, and deletion.
They do not launch an external ACP process.
The fixture also seeds a nested delegation tree through workspace storage APIs.
Browser assertions select a child from another root, switch transcripts, and
delete a grandchild through Studio while preserving its parent and root.
This checks persisted tree navigation and deletion, rather than model-driven
delegation execution.
The suite also verifies
settings scrolling at 1440px and 900px widths. Settings keep their selected
section, cached snapshot, and pending save queue across page navigation and
browser history. Tool call and result cards must
share both edges at 900px, 1440px, 2560px, and 3440px widths, collapsed and
expanded, without horizontal transcript overflow. Unhandled JavaScript errors and
unexpected HTTP errors fail the suite. A new session's expected latest-run 404
is allowed.

Failed tests retain screenshots, an error context, and Playwright traces under
`studio/frontend/test-results/` (ignored by Git). For example, from the frontend
directory:

```powershell
npx playwright show-trace test-results/<case>/trace.zip
```

The suite establishes deterministic runtime and UI behavior. It does not
measure live model quality, provider interoperability, or all visual layouts.
Windows execution checks the Windows shell and file behavior; run the same
suite on POSIX to establish its platform-specific behavior.
