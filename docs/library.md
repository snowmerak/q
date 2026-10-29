# Library and workspace coordinator

Library owns global Agent Skills and the workspace knowledge/work catalog. A
Studio project is one workspace UUID containing up to 32 ordered directory
roots. Issue, ticket, reply, member, attempt and manager inbox originals live in
one SQLite database, partitioned logically by `workspace_id`.

## Ownership and storage

Every Q process uses the configured loopback address and `library.lock`. One
process holds the OS lock, owns the listener and opens the source stores. Other
processes use HTTP. `q library start` and ordinary Q/Studio hosting use the same
server, identity checks, election and takeover implementation. Lock-file
existence alone does not prove ownership.

```text
<config>/
  library.json
  library.lock
  library/
    workspace-knowledge.sqlite        # authoritative workspace/work sources
    data/records/                    # global skill projections
    index/                           # global skill BM25/HNSW
    knowledge-index/<generation>/
      all/                           # global issue/ticket projection
      workspace-<workspace-id>/       # workspace-specific BM25/HNSW
  workspaces/<workspace-id>/
    .q/sessions/<manager-session-id>/ # protected Integrator (existing identity)
    coordinator/.q/sessions/<id>/     # protected Coordinator intake conversation
    workers/.q/sessions/<worker-id>/  # native/ACP ticket checkpoints
  worktrees/                         # existing Git worktree/MR leases
  coordinator/                       # OS locks for ticket/review operations
```

SQLite schema version 6 stores workspace metadata/roots, source items/posts,
immutable revisions, mutation receipts, the indexing outbox, projection cursors,
member references, attempts and manager inbox events. Legacy
`library/studio-projects.json` is imported once, transactionally, preserving
project IDs and timestamps. Its original file is retained as a backup. Project
removal archives metadata and preserves source records and conversations.

HTTP is loopback-only and unauthenticated; another local process is inside this
trust boundary. The former Library authentication settings are ignored and
omitted on save. Gateway authentication is separate. Protocol version 10 checks
service identity, readiness and configuration-directory filesystem identity.
An incompatible process or a different configuration directory using the same
port is reported instead of replaced. Stop older Q processes before upgrading;
separate configuration directories require separate Library ports.

## Issues, tickets and source reads

Issues have open/closed state. Tickets have exactly three board columns:
`todo`, `in_progress`, and `done`. Each item has a stable ID, a per-workspace/kind
number and an independent body-post ID. Replies have their own IDs and monotone
thread sequence numbers. Bodies and replies retain immutable revisions.

Writes commit originals, revision history, outbox entries and optional request
receipts in the same transaction. A repeated `request_id` with identical input
returns its original acknowledgement; changing input under the same key returns
409. `revision` is required for optimistic edits. `source_revision` advances on
content/metadata changes, separately from scheduling/lease changes. This keeps
System One retry keys stable while an unchanged ticket waits for assignment.
Source acceptance does not require embedding, System One or an agent response.

Thread reads use a snapshot upper sequence and keyset cursors. They return
bounded previews and `content_truncated`; `post_get`/the post-content API return
complete content in Unicode-safe pages pinned to one revision. Concurrent edits
cannot splice versions. Tombstones retain revision history and remove derived
search hits. Long source content is limited to 2 MiB per post.

## Search and embeddings

Search accepts natural-language query text and lexical, semantic or hybrid mode.
The default scope is the host-bound workspace; `all:true` explicitly selects all
workspaces. Kind, board/issue state and label filters apply in either scope.

Workspace and global BM25/HNSW stores are separate derived projections, so a
small workspace does not depend on post-filtering a global ANN candidate set.
Post chunks contain workspace, item/post IDs, source revision and character
offset. Hits are checked against current originals before being returned.
Lexical totals count matching chunks; semantic/hybrid totals are a candidate
lower bound. Cursors bind the query, scope, filters, index generation and source
cursor; changed generations/source cursors return 409 and require a fresh search.

The transactional outbox can be replayed after interruption. Each projection
has its own committed cursor. Reindex and model/dimension changes rebuild derived
stores while leaving SQLite originals intact. `index_pending` identifies source
projection lag, and `embedding_pending` identifies missing current vectors.
Hybrid retrieval explicitly reports lexical fallback when embeddings are
unavailable. Semantic-only retrieval reports unavailability instead of a
successful empty result when its configured vectors are absent.

Q's existing configured embedding client generates vectors. Each batch is
accepted only if the model, dimensions, source digest, post revision and item
revision still match. Stale provider results cannot replace a newer source.
Studio's coordinator fills missing batches in the background; no Librarian or
additional agent/model judgement runs during source writes or indexing.

ACP stdio bridges reuse the owning host's existing Gateway for query embeddings.
A private per-host connection file is refreshed when Gateway changes and removed
on clean shutdown. The bridge rereads embedding settings and connection data on
each query, validates loopback routing, and rejects responses if settings changed
during generation. It does not start a second provider. While the Gateway or
vectors are unavailable, hybrid/semantic responses keep their explicit fallback
or error behavior.

## Membership and execution

Ticket issuers should always choose `execution_mode` explicitly:

| Mode | Use | Completion |
| --- | --- | --- |
| `report` | Read-only investigation, analysis and recommendations; no project-file changes | The manager accepts the report |
| `git` | Creating or editing code/project files, including project initialization | The manager reviews and merges the pinned change request |

Git mode requires an existing Git repository with a checked-out branch, an
initial commit and no uncommitted changes. Prepare that repository before
delegating its first code task; a missing repository does not make code work a
report task. Omitted modes still default to `report` for compatibility.

In Studio, choose **Execution mode** when creating a ticket, or change it through
**Edit details → Execution mode → Save details** before execution starts. Leaving
**Requested member** empty lets System One choose. Native and ACP ticket tool
descriptions carry the same guidance; `ticket_create` should include
`"execution_mode":"git"` for code/file changes or `"execution_mode":"report"`
for read-only work. Issuance automatically requests assignment in either mode.

Workspace membership references existing global/workspace profiles by canonical
agent ID plus definition and execution roots. Profiles, permissions and ACP
connections remain in their original settings. Disabling a member prevents new
assignment and fences subsequent running checkpoints. Root changes, missing
profiles/connections, direct junior delegation and unsuitable mutation modes are
excluded before selection.

Issuing a ticket commits its source and `queued` assignment state in the same
transaction, for Studio, native tools and ACP alike. No separate Start action is
required. The coordinator checks every two seconds while Studio runs. The board
stays `todo` until an eligible worker is reserved. Older unstarted tickets with
an empty execution state are admitted automatically; explicit stops and uncertain
outcomes still require an explicit retry. Source acknowledgement does not wait
for assignment, execution or review. System One
must select an eligible joined member even when a member was explicitly
requested. Missing settings, missing credentials, failed/invalid answers and
`none` leave `assignment_waiting` with a reason and bounded retry delay. There is
no first-member or main-model fallback. Candidate batches retain every eligible
member and validate returned member IDs. Assignment HTTP calls use the same
two-minute budget as the coordinator, while caller cancellation can stop them
earlier; the general System One client default remains unchanged.

The existing GeneralRunner/delegation runtime executes native workers. Each
attempt has a distinct ID, durable worker session reference, owner/generation/
token and renewable lease. An OS lock is held for the complete ticket execution
across processes; source transitions and checkpoints are additionally fenced by
the reservation. Worker results, ticket replies and inbox events commit together.
Success moves the ticket to `awaiting_review`, not `done`.

Mutating workers require Git execution mode and an isolated worktree owned by the
existing `gitwork.Manager`. Report mode allows non-mutating profiles. A feedback
attempt retains the same worker conversation and active worktree when its member,
roots and definition/model fingerprint still match. Changed profiles cannot
silently take over an existing worktree.

Managed role sessions, ticket workers and pinned merge retries ensure `.q`
metadata is locally ignored through Git's common `info/exclude` file. This does
not edit project `.gitignore` files or hide modifications to already tracked
files. Unrelated uncommitted project changes still block preparation and merges.

ACP workers persist the external session ID, connection/definition fingerprint,
root and prompt completion marker. Follow-ups require real load/resume support;
there is no fallback to a new external session. A prompt with an unknown outcome
is never automatically replayed. Native recovery records unfinished tool
outcomes as unknown and requires explicit continuation/inspection without replaying those calls. Stop is requested durably and
cancels execution when the next fence/heartbeat observes it. Expired agent leases
become `unknown` and require an explicit retry/inspection.

## Coordinator, Integrator and MR review

Each workspace reserves two independent conversations:

- **Coordinator** receives requests, records requirements and plans in issues,
  and creates actionable tickets with acceptance criteria and explicit execution
  modes. System One still selects from joined subagents. Planning and dependent
  implementation must be issued in order; there is no ticket dependency scheduler.
- **Integrator** receives worker result notifications, reviews the pinned changes,
  requests rework or accepts/rejects results, and authorizes merges.

The existing `manager_session_id` and its storage remain the Integrator identity
for API and on-disk compatibility. Schema 6 adds a stable
`coordinator_session_id` to all existing workspaces, including archived ones;
existing history, model selections and ACP checkpoints are preserved. New
workspaces reserve both IDs in the source transaction. The UI labels these
sessions by role, independently of their conversation titles. Studio creates and
registers both and reconciles interrupted creation on later project reads. Native
and ACP sessions use the normal SessionHost/model controls and receive their
role instructions. Each has its own conversation and model/provider selection.
Their state stays in the central directory while tools/ACP execute in current project roots. Missing
roots or archived projects block execution while retaining conversation history.

The local manager marker, central reserved ID and storage filesystem identity
must agree. Workspace/CLI/catalog deletion and unregister paths reject managers.
Clear is allowed when idle, retains identity and model/provider selection, and
removes conversation/ACP resume state. Issues, tickets, attempts, MR ownership
and inbox events remain in Library.

The background scheduling engine delivers durable inbox events to the Integrator
through ordinary Studio runs with
stable operation IDs. Existing run monitoring, questions and turn controls
remain available. Failed delivery records its error and a bounded retry backoff.
Completion receipts survive conversation clear and later user turns. Manager turns
waiting for input do not consume worker capacity. Busy managers defer delivery.
Completion before an inbox
acknowledgement is safe to replay through the saved operation result. Superseded
notifications are acknowledged without executing an obsolete review.

`ticket_change_read` resolves the attempt's actual pinned MR. The ordinary
`change_request_*` tools retain their existing direct-child ownership rules.
Only the centrally reserved Integrator session may review an attempt; the source also rejects review requests carrying the Coordinator identity. Report
acceptance marks `done`. Git acceptance durably queues a real pinned merge;
`done` is committed only after Git succeeds. Dirty parents, moved bases/heads,
merge/cleanup failures and stale attempts remain visible conflicts. Merge
operations serialize per repository/ref and hold the ticket lock. The existing
exact two-parent merge recovery handles a process stopping after Git commits
but before the source acknowledgement. Rejecting Git work queues normal
worktree/MR closure. Feedback is stored as a searchable ticket reply and queues
another checked assignment to the original member.

## Native and ACP tools

The same workspace source tools are registered for native Q and `q-library` MCP:

- `issue_create/list/get/reply/update`, `ticket_create/list/get/reply/update`;
- `post_get/update`, `search_work`, `workspace_members`;
- `ticket_start/retry/cancel`, `ticket_attempt_list/get`, `ticket_change_read`, `ticket_worker_read`;
- only the Integrator receives `manager_inbox` and `ticket_review`; the Coordinator has issue/ticket tools without review authority.

The host binds workspace and actor; tool arguments cannot select another write
scope or impersonate a manager. Explicit global reads do not grant cross-project
writes. `search_skills`/`get_skill` continue to expose global skills through MCP.
Ordinary ACP connections opt in using **Q Library for Sessions**. Managers and
ticket workers always receive their required workspace tools.

```text
q library mcp --config-dir <directory> --workspace-id <id> --session-id <id> [--manager]
```

`--embedding-connection` is supplied internally by the host. Bridge stdout
contains MCP frames only. It stays initializable during Library outages and
reports individual operation failures accurately.

## Retired automatic learning

`learn`, `request_learning`, proposition read/write/search APIs, Thinker,
Librarian judgement, learning commands/UI and automatic transcript/task/size/
restore triggers have been removed. Task lifecycle, transcript persistence,
context compaction, skills and Workspace Memory remain available. Failed delivery records its error and a bounded retry backoff. Completion receipts survive conversation clear and later user turns. Manager turns waiting for input do not consume worker capacity. Verified
lessons and decisions are written explicitly into issues/comments.

Old learning metadata can be read solely to preserve historical conversations;
it is discarded and omitted on new saves. Retired model assignments are removed
on load/save and their names cannot become custom roles. Existing proposition,
queue, checkpoint and diagnostic files are retained as inert backups; they are
not replayed, judged or included in the new issue/ticket projections. No project
ownership is guessed for old global knowledge.

## HTTP surface

All Library routes use `/v1`:

| Resource | Operations |
| --- | --- |
| Health | `GET /health`, `/status` |
| Workspaces | `GET/POST /workspaces`, `PUT /workspaces/{id}`, `POST /workspaces/{id}/archive` |
| Sources | `GET/POST /items`, `GET/PUT /items/{id}`, `GET/POST /items/{id}/posts`, `GET/PUT /posts/{id}` |
| Search/index | `POST /work/search`, `GET/POST /work/embeddings`, `POST /work/reindex` |
| Membership | `GET/POST /workspaces/{id}/members` |
| Execution | `POST /items/{id}/control`, `/claim`, `GET /items/{id}/attempts`, `GET/POST /attempts/{id}`, `POST /attempts/recover` |
| Worker history | `GET /attempts/{id}/worker` (bounded public transcript and unknown-tool pages) |
| Review | `POST /attempts/{id}/review`, `/merge`, `GET /workspaces/{id}/inbox`, `POST /inbox/{id}/ack`, `/defer` |
| Skills | Existing `/skills/search`, resource, reload and embedding routes |

Studio adapts source/search routes below `/api/v1/work`, membership/inbox below
`/api/v1/projects/{id}`, and exposes Issues, Tickets, Coordinator and Integrator navigation. The role endpoints are `GET /api/v1/projects/{id}/coordinator` and `/integrator`; `/manager` remains an alias for the Integrator.
Source and execution errors preserve HTTP status; stale revisions return 409.
See [the coordinator plan](workspace-issues-tickets-plan.md) for acceptance scope
and implementation/verification status.
