# Agent Skills

q implements the portable [Agent Skills specification](https://agentskills.io/specification)
as a disk-indexed retrieval layer. It never injects the full skill catalog into
every model request.

## Storage and retrieval

Each active global skill is projected into the global Library Store. Active
workspace skills are projected into the current workspace Session Store. Both
use the same `skill` record shape:

- `summary`: skill title/name, full-text indexed
- `content`: description, full-text indexed
- `search_text`: tags, full-text indexed
- `tags`: exact tag filter
- `scope`: `global` or `project`, exact filter but not full-text search input;
  `project` is the persisted compatibility value exposed as `workspace` by
  commands and tools
- `location`: absolute checkout directory, stored in the source JSON record and
  not indexed
- `payload`: source kind, `SKILL.md` content digest, Git commit when available,
  compatibility, license, metadata, and informational `allowed-tools`

The record contains no `SKILL.md` body. `search_skills` performs one bounded
Library query for global scope and one workspace Bleve query for workspace
scope, then merges the results inside the existing MCP tool. If both queries
return the same name, the workspace hit is retained, and `total` is recomputed
from the merged, de-duplicated result set before the requested limit is applied.
Lexical ranking weights the skill name highest, then tags, then description.
When embeddings are available, lexical and semantic candidates use
reciprocal-rank fusion. A skill-specific confidence gate removes extremely weak
lexical hits only when the semantic branch has a confident candidate; otherwise
the complete lexical branch remains in the fusion.
`get_skill` routes the selected ID to the Library or local registry and always
returns the complete resource text directly in its `content` field, whether or
not Loom is available. It does not create a Loom artifact. There is no separate
Library MCP tool.

The model-facing search tool is named `search_skills` (plural). `SearchSkillHints`
is a host-side method that retrieves candidate metadata; it is not a model tool.
Every q-owned inner subagent receives `task_start`, `search_skills`, and
`get_skill` as fixed tools. This includes custom profiles, the six built-in
delegated agents, and the dedicated plan Scout, Griller, Planner, Coder, and
Planner review runners. A profile's `tools` list controls its additional tools,
not this fixed set. An inner subagent cannot start when either skill tool is
missing from its runtime catalog.

## Embedding status

Studio exposes a read-only embedding coverage query at
`GET /api/v1/embeddings/status`, also available under **Settings → Embeddings**.
It reports the configured model and dimensions, current/missing/outdated eligible
records, stored vector models, invalid vectors, unreadable files, and persisted
index configuration for the global Library and workspace archives. The optional
`workspace_root` parameter adds an absolute workspace path to the inspection.
Registered project and session roots are included automatically; existing sibling
archives in their parent directories are discovered without recursive traversal
or registration. `discovery_roots` describes that discovery scope, with at most
1,000 entries inspected per parent. This is not an inventory of every archive
on disk.

Counts use the same eligibility and projection matching rules as embedding
migration: empty text and `archive-read` records are excluded, and a partial set
of projections counts as outdated. `current` describes model/dimension/projection
coverage, not a verification that a vector represents the latest source text.
`complete: false`, per-archive `issues`, and top-level `warnings` distinguish
partial inspection from zero missing records. `state` is `current`, `pending`,
`absent`, `disabled`, or `error`; `pending` includes mismatched or missing index
metadata. Index metadata is persisted state, not a live HNSW search probe.
The response records observation start and completion times; concurrent writes
can appear across the scan, so it is not a transactional snapshot. Inspection
does not open indexes, acquire workspace leases, call the embedding provider,
or create or repair archives. The screen refreshes every 15 seconds while open
and supports manual refresh and inspection of another workspace path.

Manual repair is available through **Sync** on an archive row or **Sync all**.
`POST /api/v1/embeddings/sync` accepts `{"scope":"global"}`, or
`{"scope":"workspace","workspace_root":"<absolute path>"}`, or
`{"scope":"all","workspace_root":"<optional additional absolute path>"}`.
It returns **202 Accepted** with a job ID and a `Location` for
`GET /api/v1/embeddings/sync/{job}`. Acceptance means a process-owned background
job was started, not that migration finished. The status response also includes
the latest `sync_job`, so navigation can reconnect to its progress. Jobs expose
archive totals, completed/failed counts, records updated, and per-archive errors.
They are retained for the current Studio process, with at most 32 recent jobs;
restart clears this history. Studio shutdown cancels and drains active work.

Studio embedding model updates return 202 after durably saving a pending request.
The configured model stays unchanged while any Studio session is running. Once
all sessions are idle, Studio closes their old archive leases, blocks new session
admission through the migration, commits the requested configuration and syncs
the global Library and known workspace archives. Settings expose
`models.embedding_change` as pending, applying or failed until migration completes;
the Models screen refreshes this status automatically. A newer request replaces
the pending target. Requests survive shutdown in `embedding-change.json` and resume
on the next Studio start. An interrupted or failed migration remains unapplied in
this status and retries, retaining any completed derived embeddings. Exiting
before the idle transition leaves the configured model unchanged.

Only one manual job runs at once. An identical request reuses the active job;
a different overlapping request returns 409. Disabled embeddings return 409,
and a missing runtime returns 503. The job uses saved model settings, not model
parameters supplied by a caller. A model change during sync fails the job for
retry; provider responses generated for a superseded model are rejected. Each
archive has a five-minute deadline. Successful records remain saved when a
later batch or another archive fails, and retries fill only missing/outdated
vectors. Workspace sync targets one existing archive and does not create absent
archives. Sync all uses the same discovery scope as status, skips current and
absent archives, continues after individual failures, and reports incomplete
discovery as a failure rather than claiming full completion. Automatic repair
on normal use remains enabled. During manual sync the screen refreshes every
three seconds and returns to its ordinary refresh interval after completion.

Workspace repair verifies consecutive complete scans using source digests that
exclude derived embeddings. The final scan must match the preceding scan and
need no embedding writes or skipped conditional updates. This catches offset
shifts caused by deleting an already-current record. After five passes without
stability, repair returns an error while retaining completed updates for retry.
`BackfillStats.Scanned` includes verification reads. This is convergence checking,
not a transactional snapshot; subsequent changes are repaired on later use.
Waiting semantic operations and configuration changes honor their context while
another repair runs. Chronological and metadata-only reads bypass that wait.

## Contextual discovery

In the main chat, when both tools are available, q performs host-side candidate
retrieval at the points where new task information becomes available:

- a new user turn, using the user text and any active task objective and
  completion criteria;
- a successful `task_start`, using its objective and completion criteria;
- an `ask_to_user` answer, using the question and context together with the
  selected choice label and description or the free-form answer.

Every inner subagent loop searches automatically after a successful
`task_start`. When relevant hits exist, the tool result carries bounded
candidate metadata and tells the model to load an applicable candidate with
`get_skill`. Q requests `task_start` by name until the model starts the task.
The dedicated plan runners keep their existing completion tools
(`submit_brief`, `submit_plan`, `review_task`, or `task_complete`) and use
`task_start` for the same skill discovery step. The main chat instructions
require an initial `search_skills` query for guidance suited to the work
environment, project, and request before substantive work. Inner subagents
receive the same instruction after `task_start` for their assigned task. They
load applicable instructions with `get_skill` and search again when new
information calls for more guidance.

The normalized query is limited to 4,000 runes. Without System One reranking,
`task_start` requests at most twelve search hits and adds at most six candidates;
other contextual triggers request at most eight hits and add at most four
candidates. In the main chat it removes skill
IDs already hinted or loaded in the current context; a subagent `task_start`
removes duplicate IDs from that search result. Candidate descriptions are limited to
600 runes; each candidate retains at most twelve tags of at most 80 runes each.
In the main chat, previously seen IDs are recovered from earlier contextual hints,
`task_start`/`ask_to_user` tool results, and successful `get_skill` results.

When `systemone.json` exists, q uses its `agent_skill_decision` model assignment
or representative-model fallback to rerank Agent Skills. Contextual discovery
retrieves 32 candidates, sends one System One request containing one score
question per candidate, and keeps the top eight for `task_start` or the top six
for other contextual triggers. `search_skills` retrieves and
scores 32 candidates, then returns at most 20 or the smaller requested limit;
requests above 32 are still capped at 20 results. A failed decision falls back
to the original retrieval order and adds a warning instead of failing search.

Only candidate metadata is injected. It is explicitly marked as
non-instructional, and the model must call `get_skill` with an exact candidate
ID before following that skill. If no candidate applies, or later work reveals
a different information need, the stable agent instructions continue to direct
the model to call `search_skills` itself. Automatic retrieval errors are
non-blocking and leave that model-driven fallback available.

For a new user turn, the hint is appended to the current model-facing user
message before its first provider request. The visible transcript retains the
original user text. Hints found after `task_start` or `ask_to_user` are fields
inside those new tool results. No path inserts a dynamic developer message or
rewrites an earlier request prefix. This append-only placement preserves the
existing prefix for provider prompt caching; an actual cache hit still depends
on provider routing, retention, and model support. Codex App Server routes keep
the same conversation/thread ID across ordinary follow-up turns and delegated
tool callbacks, and contextual hinting does not reset that ID.

## Discovery and precedence

From lowest to highest precedence:

```text
~/.agents/skills
~/.q/skills
<nearest-git-root>/.agents/skills  # when distinct from <workspace>
<workspace>/.agents/skills
<workspace>/.q/skills
```

For the interactive app, `<workspace>` remains the directory where q was
started. When that directory is inside a Git work tree, q additionally reads
the nearest Git root's portable `.agents/skills` directory at lower precedence.
This does not expand the workspace root used by file tools, sessions, or other
workspace state. An ACP process applies the same rule relative to its explicit
`--root`.

Each direct child is one skill and must contain `SKILL.md`. Frontmatter `name`
is still required, must be a valid lowercase kebab-case name, and remains the
canonical identity, but it does not have to match the directory name.
`description` is optional and has no separate length limit inside the bounded
`SKILL.md` file. The later valid definition wins when names collide. The
`/skills` management catalog retains all valid entries so a shadowed global
checkout can still be pulled or removed; each scope keeps its own projection,
and merged search suppresses same-named hits when both appear in the bounded
result sets. Validation and shadowing notes remain visible in the manager.

## Git management

q manages Git checkouts in its native roots. Open `/skills`; the two panels map
to these locations:

```text
GLOBAL    -> ~/.q/skills/<skill-name>
WORKSPACE -> <workspace>/.q/skills/<skill-name>
```

Use `Tab` or the left/right arrows to focus a panel, the up/down arrows to
select, `A` to enter a repository URL and clone it, `U` to pull with
`git pull --ff-only`, `D` to remove after confirmation, and `R` to rediscover
all roots and reconcile the Bleve index. `.agents/skills` entries appear in
their corresponding panel but are read-only because q does not own them.

The cloned repository must have `SKILL.md` at its root. The destination name is
taken from validated frontmatter, not from the repository URL. Updates refuse
non-fast-forward integration.

The indexes are derived projections. For a skill inside a Git work tree, q
records the checked-out `HEAD` commit in addition to the `SKILL.md` content
digest. The Library reconciles global roots when the leader starts, after
explicit reload, and after managed global Git operations. The workspace
reconciles at startup and after workspace skill management. During a running
session, the next contextual hint, `search_skills`, or `get_skill` operation
also reconciles workspace sources when the previous successful check is at
least 30 seconds old. This check is demand-driven rather than a background
scan. A failed automatic reconciliation leaves the existing projection usable
and becomes eligible to retry after five seconds; `/skills` reload still forces
an immediate check and reports errors.

Reconciliation compares both the digest and Git commit: unchanged records are
not saved or reindexed, while a changed `SKILL.md` digest or Git commit causes
the skill record to be reindexed. Added and deleted skills are also applied.
When an embedding model is configured, active skill metadata is embedded and
searched through the rebuildable HNSW index together with BM25; without one,
search remains BM25-only. Name, description, and each tag have separate vector
projections. A matching projection contributes according to description (4),
tag (3), or name (2); the best weighted projection represents the skill in the
vector ranking before reciprocal-rank fusion with BM25. Assigning a new model
or finding an old combined skill vector backfills active skills. Model assignment
also re-embeds existing global propositions, preserving their content, queries,
provenance and registration identity. Studio migrates existing archives for all
registered project and session workspaces, even without a selected workspace.
Failed migrations report an error; retrying resumes missing or outdated vectors.
Semantic workspace searches reconcile missing and outdated record embeddings
before querying, regardless of Studio registration. Global skill/proposition
searches and proposition registration also repair their embeddings on use.
These checks run again on subsequent use, so a later import or a failed startup
migration does not require another settings save. Matching vectors are reused;
repair errors are returned and a later request retries the remaining records.
Concurrent repairs on one archive/client are serialized with cancelable waits,
and model assignment cannot change halfway through those operations.
Embedding backfills patch only derived vectors under the source-store lock.
They compare a digest of the original record before writing, so deletions are
never resurrected and concurrent source edits trigger a bounded rescan.
The Library's persisted model settings take precedence over client snapshots;
older clients adopt that model on use instead of reverting the shared graph.
Running workspace archives also resolve the latest settings on use. Supplied
query vectors with an outdated model or shape are regenerated from query text,
and requests without enough text to repair them return an error.
Library readiness probes use 300 ms, ordinary requests default to 5 seconds,
and index configuration and embedding synchronization requests allow 5 minutes
per request, subject to the caller's deadline. Workspace archive opening,
vector configuration and batch indexing use the same five-minute budget,
including automatic repair during retrieval. Git detection
is best-effort, so a
non-Git skill, an unborn repository, or an unavailable Git executable leaves
the commit empty without blocking discovery. Each search still queries only
the existing projection after any due reconciliation; it does not perform an
unbounded directory scan.

## Capability boundary

Resource paths cannot escape the resolved skill directory through `..` or
symlinks, and individual files are size bounded. The experimental
`allowed-tools` field is informational and cannot expand a role's tool set.
A successful `get_skill` result is appended to the current model context without
modifying any earlier message. Context compaction keeps the newest result for
each `(skill ID, resource path)` verbatim, newest first, up to an independent
10% of the model context window. The newest resource is kept whole even when it
alone exceeds that soft budget; resource text is never truncated. Older copies
and resources outside the budget are omitted only when compaction is applied.
