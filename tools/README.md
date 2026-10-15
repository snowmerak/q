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
- `browser_search`: metasearch → System One source selection → CDP reading and paragraph classification → accumulated evidence check after each page
- `web_search`: compatibility name for `browser_search`
- `web_search_batch`: run independent research tasks concurrently with per-task evidence, outcomes and timings

Raw browser tool registration (`browser_open`, `browser_read`, `browser_snapshot`,
`browser_click`, `browser_fill`, `browser_close`) is temporarily commented out.
These tools are absent from MCP and in-process tool lists and cannot be called.
Only the research tools and compatibility alias above are exposed for web access.
The internal `webbrowser` Rod/CDP runtime remains available to Go callers, but
configured `browser_search` now uses it as the primary page reader.
Its browser implementation uses an installed Chrome/Chromium or `Q_BROWSER_PATH`,
an isolated temporary profile, bounded page text, and cleanup on close/idle expiry.

Settings → Web Search owns page-read concurrency (1–8, default 2), source/evidence
selection and completion models, and a reserved generative model selection.
Existing System One role assignments remain authoritative and are preserved.
The generative model is saved in `config.yaml` but is not invoked or inherited
from the chat model. Web Search settings participate in selective import/export.
Changes apply on the next search; lowering capacity lets admitted reads finish.

Chrome is reused across tool runtimes in the same process. Each research call
owns one incognito context and reuses its page/cookies across source reads.
Read capacity is shared across concurrent batches and sessions; model inference
releases read capacity. Common image/media/font URL patterns are blocked. Reads
wait for DOM readiness/stability instead of all network resources. The last tool
runtime closes Chrome; with no search contexts it also expires after two minutes.
Separate OS processes have independent browser pools.

`browser_search` is registered in both MCP stdio and in-process runtimes, including
the builtin interviewer, manager, research, and senior developer profiles.
These profiles also expose `web_search_batch`; the junior developer profile
omits both search tools. Custom profiles select them explicitly in `tools`.
Request exactly one piece of information per `browser_search` call. Split lists
and multi-entity questions into separate calls or one `web_search_batch` task per
information item (for example, one ministry's current minister). Preserve source,
time and verification constraints in every subgoal, then combine the results in
the calling agent.
Initial discovery uses the pinned Go scour library to
query its eight general web engines concurrently, with an eight-second timeout
per engine. Successful results are merged and ranked even when other engines
fail; engine failures remain in the research result. At most 20 candidates are
retained, filtered by `allowed_domains`. Search snippets guide selection and
never count as source evidence. No scour answer or infobox plugins run.
System One selects one of up to eight unread candidate URLs before Q reads its
rendered original source through CDP. Q checks the publisher independently, then offers
source blocks in document order, up to four per native request. Each question
explicitly identifies its block ID; the ID is not assumed to be visible to the
model automatically. Useful partial facts are collected verbatim. Adjacent short
paragraphs, headings and dates stay together, normally around 1,600 UTF-8 bytes.
An individual long paragraph is not silently clipped.

After the page has been classified, the completion model checks the accumulated
source evidence against every requirement **and the original qualified goal**.
Source, time, identity and scope conditions remain in the completion input even
when a caller supplies short requirements. Missing evidence continues to another
candidate from the same metasearch list. Candidate exhaustion returns incomplete
evidence. This path performs no generative planning, query refinement, browser
rendering, form interaction or navigation through page links. HTTP failures are
recorded and skipped; no Chrome process is started by research.

Static extraction prioritizes semantic main content while retaining navigation
URLs, including links inside menus omitted from source prose. Image `alt` text
is retained as publisher-provided text; no image recognition is performed.
Selection requests include only the currently selectable search candidates,
with bounded snippet previews. Full candidates remain in the stored result.

Within each research call, Q hashes whitespace-normalized extracted source text
with SHA-256. Unread aliases or redirects with an already seen body are recorded
as `unchanged` and add no repeated evidence. Requested URLs and redirect
destinations are remembered, and fragments/common tracking parameters are
excluded from URL identity. `last_page.content_hash` and action `target`/`outcome`
fields expose the comparison. Source passages are not replaced by repeated
whitespace-only responses.

System One requires explicit `web_search` and `web_search_finish` entries in
global `systemone.json` `role_models`. Choose them independently in Studio's
Settings → System One panel as Web Search Navigation and Web Search Completion.
`browser_search`, its `web_search` compatibility name and `web_search_batch` are
hidden until both assignments name configured providers and models. Clearing
either assignment hides all three tools and rejects direct calls; missing or
invalid settings also hide them. Existing
in-process sessions refresh the catalog when search becomes configured; MCP
clients see the current configuration when they request `tools/list`. No model
endpoint is contacted to decide availability. The default model alone does not
enable web search. For Gemma
navigation/source checks and Qwen completion checks, add these assignments to
the existing role map:

```json
{"web_search": "oev/gemma4-e4b", "web_search_finish": "oev/qwen3.8-27b"}
```

The navigation role selects pages, checks publishers and classifies source
blocks. The completion role checks native `supported`/`missing` questions once
after each accepted page, never between its paragraphs. Zero collected evidence
cannot complete a task. HTTP-failed, rejected and unchanged pages contribute no
evidence and advance to the next candidate. All source/collection/coverage errors
retain partial results. The models are not hard-coded and settings are not
rewritten; the usual global directory is `~/.q`. No calling chat model is needed
for this workflow. `planner_calls` remains in the result for compatibility and
is zero for configured research tools.

Embedders can inject a concurrent service with `tools.WithWebSearch`. Existing
direct `websearch.Engine` embedders that omit `Discovery` retain the older
injected browser/planner loop. Configured MCP and in-process research tools
always supply `ScourSearcher` and a shared CDP reader. With Discovery,
an optional `NewSession` injection supplies `Page(ctx, url, false)` and `Close`
only; without it Q reads via Go HTTP and creates no browser resources.

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
`requirements`, the whole goal is one requirement. `start_url` bypasses initial
selection; if that page is insufficient, one metasearch list is fetched unless
the page budget is already exhausted.
The result contains verbatim passages with URLs, unresolved requirements,
action history and System One/planner call counts. Source acceptance and
completion are separate System One assessments; completion checks both the
requirements and the original goal to catch missing qualifications. Results
include `search_calls`, ranked `candidates`, `coverage_model` and `coverage_calls`; `system_one_calls`
counts all native calls, including coverage. `pages` records URL/title, block
counts, examined/added counts, outcome, truncation and error for each attempted
page; `steps` counts attempted pages. Completion checks reduce premature
completion but remain model judgments, especially for
subtle qualifications. `allowed_domains` enforces evidence publisher hosts in
code, including redirects, and filters metasearch candidates to those publishers.
`system_one_timings` records each admitted native request's phase, model, input
bytes, local admission wait and remote request time (including any server queue).
`last_observation` retains the final observation or failure. The bounded error
list keeps the latest eight errors so early search failures cannot hide a later
model timeout.

Completion checks allow 30 minutes per native request; a separately configured
completion HTTP client allows 30 minutes plus five seconds. Page selection, publisher and relevance
decisions have no independent timeout or HTTP client timeout: they inherit the
search/caller context, including model-server queueing. There is no tool input
for overriding decision timeouts. The overall search allows 35 minutes, subject
to the caller's deadline. CDP navigation/reading allows 30 seconds after page-read admission. The
smoke driver allows 36 minutes so it does not cancel the completion early.

Defaults: 20 attempted pages (maximum 40), 35 minutes total, six requirements,
up to 64 source blocks examined per page and 64 retained evidence blocks.
Every native request must fit the client's 64 KiB JSON wire contract. Before
retaining a block Q checks the future completion request including metadata and
all questions; relevant blocks that cannot fit are explicitly marked in the page
record, never silently dropped. Oversized classification batches shrink to fewer
blocks. A single oversized paragraph yields partial evidence with `evidence_budget`.
Provider tokenizer/context limits are independent of this byte check: model
errors retain structured provider `detail`, including token-limit rejections.
Configured CDP reading retains up to 250,000 text characters; HTTP-only embedders retain the 2 MiB/250,000-byte static limits. Page/block truncation is
visible in `pages[].truncated`.
Search engine blocking, unavailable models, unsupported documents and budget
exhaustion return partial evidence with `status: "incomplete"` and a stop reason.
CAPTCHAs and login can still prevent reading; JavaScript navigation and cookie redirects run in Chrome.
Results pass through normal Loom capture.

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
It schedules the same page-at-a-time research service and role models. It does not
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
It validates the entire input before starting work. Each task owns its candidate,
hash, passage, requirement and budget state. Batch selection/publisher/relevance
decisions inherit the batch/caller context without an independent timeout,
including model-server queueing. Completion remains 30 minutes.
This concurrency limit applies to one batch; other
tool calls have independent limits, and model endpoints may serialize requests.
`model_concurrency` separately limits active model requests to 1–4 (default 1,
capped by loop concurrency). Browser/HTTP work and loop state remain independent
while native decisions and completion checks share model
slots. Local model admission waits count against the 35-minute batch deadline;
completion request deadlines start after admission and also respect that deadline.
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
