# q MCP tools

`tools.NewServer(root)` creates an MCP server whose builtin filesystem tools
are jailed to `root`. `tools.RunStdio` exposes that server over standard MCP
stdio transport, and `cmd/q-mcp` is a runnable entry point:

```text
go run ./cmd/q-mcp -root C:\path\to\workspace
```

Builtin tools:

- `read_file`: bounded text reads with `LINE#HASH:` anchors
- `edit_file`: atomic, stale-safe `replace`, `append`, and `prepend` edits
- `write_file`: atomically create or replace a complete file
- `list_directory`, `create_directory` (`list_directory` applies the workspace-root `.qignore` and always omits q-owned root `.q` metadata)
- `move_path`, `copy_path`, `remove_path`
- `run_command`: start a shell command in a workspace directory
- `cmd_status`: poll command state and incremental output
- `wait`: wait up to 60 seconds for command progress or completion
- `browser_open`, `browser_read`: open HTTP/HTTPS pages and read rendered text and links
- `browser_snapshot`: inspect visible interactive elements and obtain refs
- `browser_click`, `browser_fill`: interact using refs from the latest snapshot
- `browser_close`: close a tab and stop Chrome immediately when the last tab closes
- `web_search`: System One research loop with source passages and requirement coverage; Qwen writes queries or decomposes the goal only when requested by System One
- `web_search_batch`: run independent research tasks concurrently with per-task evidence, outcomes and timings

Browser tools use Rod inside Q and a locally installed Chrome/Chromium (including
compatible Edge installations). No Docker, Node.js, Python, or browser download
is required. Set `Q_BROWSER_PATH` to an executable path when automatic detection
does not find the desired browser. A separate temporary headless profile starts
only on the first `browser_open`. Tabs and cookies survive between tool calls;
two minutes without any browser tool call terminates Chrome and expires all tab
IDs. The tool runtime and MCP stdio server also close the browser on shutdown.

Open a page, read it, then take a snapshot before clicking or filling a field.
Every snapshot replaces its element refs; every interaction invalidates them.
Take a new snapshot before the next interaction. References from replaced or
detached DOM nodes are rejected. Web text is untrusted evidence, not instructions.
Text is capped at 24,000 Unicode characters, links and interactive elements at
100, and open tabs at 8. Full tool results use Q's normal Loom capture path.
This initial implementation inspects the main document only, excluding iframe
and shadow-root contents. It supplies browser navigation rather than a search
index; `web_search` supplies a bounded research workflow on top. The existing
ACP web-search agent remains available for search delegation.

`web_search` is registered in both MCP stdio and in-process runtimes, including
the builtin interviewer, manager, research, and senior developer profiles.
These profiles also expose `web_search_batch`; the junior developer profile
omits both search tools. Custom profiles select them explicitly in `tools`.
Static documents are fetched and extracted in Go. System One chooses links,
passages, whether to inspect browser controls,
whether to fill a search field, and whether to submit or click its search
button. Input values come from the current query or requirements; new text is
written by the planner. Eligible controls are text/search inputs identified by
search semantics in GET forms or standalone search inputs. Every interaction
refreshes refs and submission rechecks the live form. This restricts the offered
actions, but it is not a browser network sandbox: page JavaScript still runs.
Each search owns a separate temporary browser session and closes it on return,
including errors and cancellation; no persistent browser daemon is required.

System One requires explicit `web_search` and `web_search_finish` entries in
global `systemone.json` `role_models`. Choose them independently in Studio's
Settings → System One panel as Web Search Navigation and Web Search Completion.
Both `web_search` and `web_search_batch` are hidden until both assignments name
configured providers and models. Clearing either assignment hides both tools
and rejects direct calls; missing or invalid settings also hide them. Existing
in-process sessions refresh the catalog when search becomes configured; MCP
clients see the current configuration when they request `tools/list`. No model
endpoint is contacted to decide availability. The default model alone does not
enable web search. For Gemma
navigation/source checks and Qwen completion checks, add these assignments to
the existing role map:

```json
{"web_search": "oev/gemma4-e4b", "web_search_finish": "oev/qwen3.8-27b"}
```

Gemma still selects the `finish` action; Qwen checks each requirement and the
original goal through native System One `supported`/`missing` choices. The same
Qwen check runs at the action-budget boundary. When System One requests `query`
or `decompose`, generation reuses the calling agent's existing model client,
model, reasoning setting and provider request options. Main TUI/Studio/ACP loops
bind their active model; delegated agents bind their selected role model,
including a successful model-group fallback. Parallel calls keep separate
bindings through MCP. No provider is opened directly, and no model or reasoning
level is hard-coded. Generated requests use isolated messages and do not reuse
the caller's stateful conversation ID. The caller's client and search context
control waiting; there is no separate 90-second generation timeout.

`q-mcp` uses its configured Q default chat client/model when no calling loop is
present. A standalone embedded runtime can supply `client.WithToolModel` on its
call context. Without a model client, navigation and evidence collection still
work; a requested generation operation returns a planner error with partial
evidence. The former `websearch.json` planner override is no longer read.
Navigation and completion checks continue to use native System One choices.
No role or provider settings are rewritten. The usual global directory is
`~/.q`. Embedders can inject a concurrent service with `tools.WithWebSearch`.

Example tool arguments:

```json
{
  "goal": "Find official Go documentation explaining CancelFunc cleanup and whether it waits for work to stop.",
  "query": "Go context CancelFunc documentation",
  "requirements": ["Why callers should cancel", "Whether cancellation waits for work to stop"],
  "allowed_domains": ["pkg.go.dev", "go.dev"],
  "max_steps": 20
}
```

Without `query`, the initial search uses the goal directly. Without
`requirements`, the whole goal starts as one requirement and System One may
request decomposition. `start_url` can bypass initial DuckDuckGo search.
The result contains verbatim passages with URLs, unresolved requirements,
action history and System One/planner call counts. Source acceptance and
completion are separate System One assessments; completion checks both the
requirements and the original goal to catch incomplete decompositions. Results
include `coverage_model` and `coverage_calls`; `system_one_calls`
counts all native calls, including coverage. `planner_calls` counts only
generative query/decomposition operations. Completion checks reduce premature
completion but remain model judgments, especially for
subtle qualifications. `allowed_domains` enforces evidence publisher hosts in
code, including redirects, and filters link destinations to those publishers or
known search result pages. It does not restrict browser subresource requests.

Completion checks allow 30 minutes per native request; the completion HTTP
client allows 30 minutes plus five seconds. Ordinary single-search action/source
decisions default to one minute; `decision_timeout_seconds` overrides that limit
up to 1800 seconds. The overall search allows 35 minutes, subject to the
caller's deadline, and its temporary browser idle timeout is 36 minutes. The
smoke driver allows 36 minutes so it does not cancel the completion early.

Defaults: 20 actions (maximum 40), 35 minutes total, at most three planner
calls, six requirements, eight passages of at most 1,400 UTF-8 bytes each,
and at most 36 action choices per decision. Static HTML is limited to 2 MiB
and 250,000 text bytes; rendered observations use the browser limits above.
Search engine blocking, unavailable models, unsupported documents and budget
exhaustion return partial evidence with `status: "incomplete"` and a stop reason.
CAPTCHAs, login, iframe/shadow controls, arbitrary forms and file upload are
not supported. Results pass through normal Loom capture.

For an opt-in real MCP/model smoke test:

```text
go run ./scripts/websearchsmoke -input docs/experiments/web-search-tool-go-input.json -output result.json
```

This uses the configured endpoints and makes real network/model calls. The
temporary smoke workspace and its Loom store are deleted after the call; the
saved receipt retains inline evidence, while its Loom reference is ephemeral.
When `-output` is supplied, the complete captured payload is also exported as
`<output>.artifact.json` before cleanup, including large preview-only results.

### Parallel research

Use `web_search_batch` when the caller already has independent research goals.
It schedules the existing `web_search` service, preserving the Gemma navigation,
native completion and optional caller-model query/decomposition flow. It does not
create another AgentLoop, automatically invent tasks, or merge sources across
tasks. Splitting a larger user request into tasks is the caller's responsibility.

```json
{
  "tasks": [
    {"id": "go", "search": {"goal": "Find official Go CancelFunc cleanup and waiting behavior.", "allowed_domains": ["go.dev", "pkg.go.dev"]}},
    {"id": "sqlite", "search": {"goal": "Find official SQLite WAL filesystem and read-only access conditions.", "allowed_domains": ["sqlite.org"]}},
    {"id": "python", "search": {"goal": "Find official asyncio.timeout exception, catch scope and rescheduling behavior.", "allowed_domains": ["docs.python.org"]}}
  ],
  "concurrency": 3
}
```

The batch accepts 1–8 tasks with unique ASCII IDs (letters, digits, dot, underscore,
hyphen; at most 64 bytes) and 1–4 concurrent loops (default 3).
It validates the entire input before starting work. Each task owns its browser,
refs, cookies, history, passages, requirements and budgets. There is no shared
browser lock between tasks. Batch navigation/source decisions default to 30
minutes, including model-server queueing; set a task's
`search.decision_timeout_seconds` to override it. Completion remains 30 minutes.
This concurrency limit applies to one batch; other
tool calls have independent limits, and model endpoints may serialize requests.
`model_concurrency` separately limits active model requests to 1–4 (default 1,
capped by loop concurrency). Browser/HTTP work and loop state remain independent
while native decisions, completion checks and generative planning share model
slots. Local model admission waits count against the 35-minute batch deadline;
native request deadlines start after admission and also respect that deadline.
Increase model concurrency only when the endpoint supports overlapping inference.
Injected services using `websearch.Engine` inherit this limit; other services can
use `websearch.AcquireModelSlot(ctx)` around each model call.

The call waits for all admitted tasks. A failure returns an item-level error and
does not cancel other tasks. Results remain in input order even when completion
order differs. Each item has `id`, `status`, `started`, `elapsed_seconds`, optional
`result` containing the normal search output, and optional `error` with a stable
code (`search_error`, `invalid_result`, `cancelled`, `deadline_exceeded`). Partial
evidence is retained when the search service returns it. Batch `status` is
`complete` only when all items are complete, otherwise `incomplete`.
`overview` precedes the large `results` body and contains every task's status,
started flag, elapsed time, evidence/unresolved/error counts. This keeps all
outcomes visible even when normal Loom capture returns a bounded preview.

The 35-minute batch deadline includes queue time; queued tasks may have less
than their nominal search budget. Cancellation stops admission, cancels started
searches, and joins them before returning. The batch owns no detached work or
persistent jobs. A caller deadline shorter than 35 minutes also applies; caller
cancellation may prevent delivery of the partial MCP response. An injected
`WithWebSearch` service must support concurrent calls and honor cancellation.

Item elapsed time measures its started search, excluding queue time; batch
elapsed time is wall time including the queue and all worker cleanup. Call counts
are summed separately. Summing item times does not measure serial latency or
establish a speedup, especially when model requests queue or reuse cached input.
The whole result follows normal Loom capture.

For a real three-task smoke run:

```text
go run ./scripts/websearchsmoke -tool web_search_batch -input docs/experiments/web-search-batch-input.json -output batch-result.json
```

Every path is relative to the configured workspace unless it is an absolute
path already inside that workspace. Lexical `..` escapes and symlink escapes
are rejected. Hashline edits should always use anchors from the latest
`read_file` result.

`.qignore` is a discovery filter, not an access-control boundary. It supports
blank lines, `#` comments, `!` negation, leading `/` root anchoring, trailing
`/` directory matching, and `*`, `**`, and `?` wildcards. An ignored directory
will not be discovered from its parent, while an explicit `read_file` or
`list_directory` path can still inspect it. Loom transforms over captured
directory listings naturally receive the already-filtered entry set.

`run_command` is asynchronous and returns a `command_id`. Pass the returned
`next_offset` to `cmd_status` or `wait` to receive only new output. Command
output is retained up to 4 MiB and returned in chunks of at most 256 KiB.
