# Global Library plan

## Current implementation baseline

The first runnable slice implements the shared server component, `q library`
foreground hosting, ordinary q in-process hosting, fixed-port discovery,
exclusive leader ownership, failure takeover, Gateway-compatible bearer
authentication, configurable wildcard/non-loopback binding, and persistent
global Session Store ownership. `/v1/health` and authenticated `/v1/status`
are available for lifecycle verification. Authenticated global Agent Skill
search, resource reads, and explicit reconciliation are available through the
`/v1/skills/*` routes and are consumed by the existing MCP skill tools.

Proposition extraction, authenticated single-record writes, persistent write
idempotency, BM25 search, and `created_at` recency ranking are implemented.
Embedding generation and multi-vector hybrid search remain later stages.

## Purpose and decisions

`q library` provides one user-level service for durable information that must
be available across workspaces. It is not a replacement for the workspace
Session Store.

The initial Library owns:

- global Agent Skill projections and access to their resources;
- global `proposition` records extracted from conversations;
- generated retrieval queries for each proposition;
- the Bleve BM25 and HNSW projections used to retrieve those records.

The initial Library does not own raw conversations, tool results, plan
checkpoints, Loom artifacts, LSP state, or project-scoped Agent Skills. Those
remain workspace-local where they already exist. q does not create or store
workspace-specific propositions; proposition memory has one global scope only.

Propositions initially track only their immutable `created_at` time. The
Library does not attempt global equivalence, reconfirmation, contradiction,
supersession, or validity-window tracking. Those operations require comparing
an ever-growing corpus and are deliberately outside the first implementation.

## Process and ownership model

Every Library-aware q process uses the same configured address and the same
user-level lock file. At most one local process is the Library leader. The
leader holds the OS file lock for its entire lifetime, owns the TCP listener,
and is the only process allowed to open the global Store read-write. All other
processes are HTTP clients and never open the Store directly.

The same Library server component supports two hosting forms:

- `q library` runs it as a dedicated foreground service;
- an ordinary `q` session embeds it in-process on a background goroutine when
  no compatible service exists and that q process wins the leader lock.

These are two entry points to the same server implementation, Store ownership,
HTTP routes, authentication, and election protocol. An ordinary q installation
therefore needs no second executable or separately installed daemon. Running
only `q` is enough to make the Library available, while `q library` is the
explicit form for users who want its lifetime independent of a workspace TUI.

```text
q workspace A (possible embedded leader) --\
q workspace B -------------------------+-- HTTP --> global Store
q library (possible dedicated leader) ----/           |
                                                library.lock
```

The global files live below the personal q configuration directory:

```text
~/.q/
|- library.json
|- library.key
|- library.lock
`- library/
   |- data/records/
   `- index/
      |- bleve/
      |- vectors.hnsw
      |- vectors.ids.json
      `- state.json
```

The lock is an operating-system lock held on an open file handle. Lock-file
existence is diagnostic metadata only and never proves ownership. The metadata
records the PID, host, start time, q version, configured listener, and leader
generation for diagnostics.

## Listener and authentication

The Library has a configured, stable TCP port so every q process has a common
rendezvous point. Its listen host is configurable and may expose the Library
outside the local machine. Wildcard and non-loopback addresses are supported;
the implementation must not force `127.0.0.1`.

Library data endpoints use the existing Gateway API-key design:

- `Authorization: Bearer <key>`;
- keyed BLAKE3 hashes rather than plaintext server-side keys;
- key IDs, aliases, creation time, and revocation time;
- constant-time verification through the existing authenticator;
- live keyring reload where practical;
- the same JSON authentication-error shape as the Gateway.

The implementation shares the Gateway authentication code and record format,
not the Gateway's settings. Library key records live in `library.json`, and
their keyed-hash master lives in `library.key`. The Library never reads or
mutates `gateway.json` or `gateway.key`. `library.key` is server-side hashing
material, not a bearer token; plaintext Library keys are shown only when they
are generated and reach clients through an explicit client setting or
environment variable such as `Q_LIBRARY_API_KEY`.

The listener configuration must distinguish the bind address from the local
rendezvous URL when necessary. For example, a server may bind to
`0.0.0.0:<port>` while local election participants probe
`http://127.0.0.1:<port>`. A q process configured to use a Library on another
machine is client-only: it cannot participate in that machine's file-lock
election or take over the remote service.

## Discovery and leader election

Both hosting forms use one connect-first, elect-second protocol:

1. Call the configured local identity/health endpoint with a short timeout.
2. If it reports a compatible, ready q Library, connect as a client.
3. If connection fails, try to acquire `library.lock` exclusively.
4. A process that acquires the lock probes the endpoint again to close the race
   between the first probe and lock acquisition.
5. If no compatible server exists, bind the configured port, open the global
   Store, finish index recovery, start the shared Library server component,
   and then advertise readiness. An ordinary q hosts it in-process; `q
   library` hosts it in the foreground.
6. A process that loses the lock race waits for the winner to become ready,
   using bounded exponential backoff with jitter, and then connects.

The health response identifies the service, protocol version, store ID,
instance generation, readiness, and q version. Merely accepting TCP
connections does not identify a q Library. If the fixed port is occupied by an
unrelated or incompatible service, q reports a port collision and does not
silently choose another port; changing the rendezvous port would split clients
between different global stores.

The leader initializes resources in this order:

```text
exclusive lock
  -> fixed-port listener
  -> global Store and index recovery
  -> HTTP routes
  -> ready
```

It shuts down in reverse order and releases the lock last. A process that owns
the lock but cannot bind the configured port must close any partially opened
resources and release the lock before returning an error.

An embedded leader is intentionally tied to its owning q session. When that q
process exits, it gracefully closes the Library, and another active or newly
started q process may take over. A dedicated `q library` process is the way to
keep the service available without keeping a workspace session open. The
initial implementation does not need to spawn a child process: embedding and
foreground hosting share the same Go component directly.

## Failure detection and takeover

There is no separate heartbeat election protocol. Normal requests and the
health endpoint detect leader failure. On connection refusal, reset, or a
failed readiness check, a local managed client discards its connection and
runs the same connect-or-elect procedure again.

Processes that fail to acquire the lock must not spin. They wait with short,
bounded exponential backoff and jitter while the winner starts. A client-only
remote configuration retries the remote endpoint but never attempts local
leadership for it.

Reads may be retried after reconnecting. Mutating requests need a caller-made
idempotency key. The Library persists the relationship between that key and
the completed result so a response lost during leader failure does not create
duplicate propositions or repeat a skill mutation after takeover.

## Proposition record

`sessionstore.KindProposition` is added to the common record envelope. A
proposition is an atomic, reusable statement rather than a transcript excerpt.
The source record remains the durable source of truth; all search structures
are rebuildable projections.

```text
kind:        proposition
content:     canonical proposition text
search_text: generated queries joined for BM25 indexing
created_at:  immutable extraction time
refs:        source conversation provenance when available
payload:
  queries:            generated retrieval queries
  confidence:         extractor confidence
  extractor_model:    model identifier
  extractor_version:  prompt/schema version
```

The initial schema has no `last_confirmed_at`, `valid_from`, `valid_until`,
`superseded_at`, or workspace scope. Repeated or conflicting statements may
produce separate proposition records. Search-time result collapsing controls
duplicate presentation without pretending to maintain a complete global
truth graph.

Secrets, credentials, raw environment values, and other configured sensitive
classes must be rejected before persistence. The extraction response is
strictly schema validated and bounded in proposition count, proposition
length, generated-query count, and query length.

## Extraction pipeline

Extraction is not performed for every message or completed turn. A durable
workspace-local state machine accumulates filtered conversation events after a
committed cursor, closes non-overlapping learning segments, and sends only
closed segments to the Thinker. The persisted state contains the stream ID,
next and committed cursors, uncommitted events, and a FIFO segment queue.

The extraction model uses the built-in `thinker` role. Like other role models,
it is configurable from `/model` or `agents.roles.thinker` and inherits the
active chat model when no override is present. Thinker jobs are serialized so
their idempotency slots and source order remain stable. A segment checkpoint is
committed only after the Thinker successfully calls `thinking_complete`; a
failure leaves the same queue head and deterministic segment ID available for
retry after restart or the next trigger.

With no existing checkpoint, accumulation starts at the latest context
compaction summary, or at the first user message when no summary exists. A
segment closes under exactly four conditions:

1. adding an event reaches 45% of the Thinker model's advertised context;
2. a validated `task_complete` result is appended;
3. a plan is approved and its complete plan payload is appended;
4. the user enters `/learn` or the agent calls the existing q-tools MCP
   `learn` tool.

If adding the next ordinary message would exceed 45%, the segment closes
before that message and the message starts the next segment. Exact 45% includes
the event and closes the segment. A single oversized event is shortened only
when it is prepared for the Thinker. The remaining 55% is reserved for the
system prompt, tool schemas, and repeated registration exchanges.

Ordinary tool-result messages and assistant `tool_calls` are excluded entirely;
an assistant message containing only tool calls is excluded, while accompanying
assistant prose remains. A successful `task_complete` result is the sole tool
result exception and is retained as a complete special event. Plan approval is
also a special event containing the approval marker, objective, and complete
approved plan; execution output starts the following segment.

Both explicit controls are fire-and-forget. They immediately acknowledge that
learning was enqueued and close the current segment when it contains eligible
context. If the segment is empty, the worker performs no model call, creates no
empty segment, and does not advance the checkpoint. The MCP `learn` call and
its result are themselves excluded like other tool traffic.

For a closed segment:

```text
checkpoint-bounded conversation segment
  -> proposition/query extraction
  -> schema and sensitive-data validation
  -> one register_proposition call per proposition
  -> idempotent Library write and BM25/HNSW update
```

The submitted context is bounded independently of the full workspace archive.
Its provenance identifies the originating workspace, run, and source records,
but the persisted proposition itself is global. Other workspaces may retrieve
the proposition even when they cannot open its workspace-local source records.

The Library should own final validation, persistence, and indexing so all
callers produce the same durable format. The exact division of model execution
between the caller and Library may be implemented in stages, but only validated
Library records become searchable.

## Text and vector projections

BM25 indexes the canonical proposition through `content` and all generated
queries through `search_text`. An empty generated-query list is valid but
reduces retrieval recall.

HNSW needs more than the current one-vector-per-record layout. Each proposition
produces bounded vector variants that resolve back to one record:

```text
<proposition-id>#content  -> <proposition-id>
<proposition-id>#query-0  -> <proposition-id>
<proposition-id>#query-1  -> <proposition-id>
...
```

Vector variants are derived data and are not separate Session Store records.
Deleting or rebuilding a proposition projection removes and regenerates all
of its variants together. Candidate results are collapsed by proposition ID
before they are returned.

Proposition retrieval performs:

```text
BM25 candidates ----------------\
                                 +-- rank fusion -- created_at decay -- top K
HNSW variants -- record collapse /
```

BM25 and vector scores are not added directly. Their independently ranked
candidate lists are combined with reciprocal-rank fusion. A configurable
half-life decay based only on immutable `created_at` then boosts newer
propositions. Time affects ranking, not eligibility: an old proposition can
still win when it is substantially more relevant.

## Agent Skills

The Library becomes the owner of global Agent Skill search projections for
`~/.agents/skills` and `~/.q/skills`. It discovers and reconciles those roots
at leader startup and after explicit install, update, remove, or reload
operations. `search_skills` performs only a Library BM25 query; it never scans
the filesystem, parses YAML, or reconciles records per query.

Project roots under `<workspace>/.agents/skills` and
`<workspace>/.q/skills` remain workspace-side in the initial implementation.
The q client may merge their results with global Library results, but the
Library does not persist them or expose them to unrelated workspaces.

When `get_skill` selects a global skill, the Library validates and reads the
skill-relative resource. The receiving q process captures the response in its
workspace Loom store, preserving the existing rule that model-facing resource
content passes through Loom rather than becoming permanent Library content.

## Initial HTTP surface

The exact encoding may use the existing MCP/JSON conventions, but the service
boundary needs at least these operations:

```text
GET  /v1/health
POST /v1/propositions
POST /v1/propositions/search
GET  /v1/propositions/{id}
DELETE /v1/propositions/{id}
POST /v1/skills/search
GET  /v1/skills/{id}/resources/{path...}
POST /v1/skills/reload
```

Health exposes no stored content. All content, search, extraction, and
management routes require Gateway-compatible bearer authentication. Mutating
routes require idempotency keys and bounded request bodies.

The global skill routes are implemented as the first data slice. Leader
startup and `/skills/reload` reconcile file digests against the persisted
projection; they do not blindly rewrite every skill. `search_skills` and
resource reads never trigger discovery or reconciliation.

Global proposition reads are also implemented. `POST /propositions/search`
queries only records with `kind=proposition` and `scope=global`; it searches
both canonical `content` and generated-query `search_text`. The default ranking
applies a `created_at` boost with weight `0.25` and a 720-hour half-life. A
request may override both values, and an explicit zero weight disables the
boost. `GET /propositions/{id}` returns the canonical text, provenance refs,
tags, and extraction payload. Both routes require Library API-key
authentication.

`POST /propositions` is also implemented for the Thinker-local
`register_proposition` tool. It requires an `Idempotency-Key`, validates the
bounded single-proposition schema and obvious credential-like content, forces
`kind=proposition` and `scope=global`, and persists the canonical content plus
generated-query `search_text`. Reusing a key with identical input returns the
existing ID; different input returns HTTP 409.

The existing `q-tools` MCP server exposes the reads as
`search_propositions` and `get_proposition`; there is no separate proposition
MCP server. Search results are captured through the normal workspace Loom
boundary. The mutation tool is private to the Thinker runner rather than being
added to the general MCP catalog.

The registration API can atomically accept a bounded embedding batch ordered
as canonical content followed by one vector per normalized retrieval query.
The Library assigns stable `content`, `query-0`, ... projection IDs, persists
them inside the proposition source record, and adds every matching projection
to HNSW. The configured global `embedding.model` and `embedding.dimensions`
select the Library graph shape at leader startup. Updates and deletes rebuild
the derived graph because the current pure-Go HNSW implementation has no
in-place deletion. `DELETE /propositions/{id}` is idempotent and removes the
source record, BM25 document, and all vector variants together.

Semantic search accepts one query embedding, collapses HNSW variants by source
proposition before reciprocal-rank fusion with BM25, and then applies the
existing immutable-`created_at` boost. The shared Library client uses q's
configured embedding provider to generate the registration batch and search
query vector automatically. Consequently the private Thinker
`register_proposition` tool and the existing MCP `search_propositions` tool use
the same semantic path without exposing vectors to either model-facing schema.
When embedding configuration is absent, both retain BM25-only behavior.

Embedding vectors are excluded from the idempotency digest because they are
derived data and may differ slightly when an identical logical request is
retried. The canonical content, normalized queries, metadata, tags, and refs
still define request identity.

## CLI and runtime integration

`q library` runs the shared Library component as a dedicated foreground
service. If another compatible leader is already serving the configured
address, it reports that instance instead of opening a second Store. Ordinary
q sessions call the same ensure-and-connect component during startup. If no
leader exists and the session wins the lock, it starts the component in-process
and uses its HTTP client surface like every other session. Its embedded Library
remains alive until that q process closes; another active session can take over
after graceful shutdown or failure.

Callers do not bypass HTTP merely because they host the embedded leader. Using
the same authenticated client path avoids a separate in-process behavior and
keeps dedicated, embedded, local, and remote operation equivalent. Internal
health and lifecycle coordination may use direct handles, but data operations
continue through the public Library service boundary.

`q library config` opens the standalone Library listener settings, and
`/library` opens the same settings from the regular q TUI. Both edit the
default bind host and fixed rendezvous port in `library.json`. Port `0` is not
valid for the Library because every process must be able to find the same
endpoint. Listener changes take effect after the current Library leader is
restarted.

Configuration includes the listen host, fixed port, local rendezvous URL when
needed, remote/client-only mode, and API-key source. The Thinker model is
configured through q's agent-role settings. The Library currently uses q's
global embedding model and dimensions for its HNSW index; listener and
index-shape changes require a Library restart. Library API-key changes use
the same live-reload behavior as Gateway keys while remaining in the Library's
independent configuration.

The Library must fail independently from workspace startup where possible. A
workspace remains usable without global memory, while its status clearly says
that global skill/proposition retrieval and extraction are unavailable.

## Implementation stages

1. Reuse the API-key authentication format and verifier, while keeping Library
   and Gateway settings and master keys independent; extract the OS lock
   primitive without changing existing Gateway or workspace behavior.
2. Add the reusable Library server component, configuration, identity/health,
   fixed-port serving, connection, leader election, startup waiting, shutdown,
   and takeover tests; host the same component from `q library` and ordinary q.
3. Add the global Store owner and read/write HTTP operations with persistent
   idempotency records.
4. Move global Agent Skill synchronization and retrieval behind the Library;
   keep project skills workspace-local and remove any per-query reconciliation.
5. Add `KindProposition`, bounded extraction records, generated BM25 query
   text, and proposition search without vectors.
6. Generalize HNSW projection IDs to support multiple vector variants per
   proposition, add hybrid RRF retrieval, and connect automatic embedding to
   Thinker registration and MCP proposition search. Implemented.
7. Add immutable `created_at` decay, extraction gating, failure recovery, and
   TUI/CLI status surfaces.

## Verification

The implementation is complete only when tests cover:

- two processes starting concurrently and exactly one becoming leader;
- an ordinary q session becoming an embedded leader without another binary;
- `q library` hosting the same server as a dedicated foreground process;
- a second `q library` detecting and reporting the existing compatible leader;
- losers connecting while the winner is still initializing;
- embedded-leader shutdown followed by takeover without Store corruption;
- leader termination followed by bounded takeover and successful retry;
- a fixed-port collision with a non-Library service;
- non-loopback and wildcard binding with authenticated access;
- missing, invalid, and revoked Gateway-compatible Library API keys, including
  rejection of keys configured only for the Gateway;
- duplicate mutating requests before and after leader takeover;
- Store/index recovery while the lock remains exclusively owned;
- global skills changing only on startup or explicit management/reload;
- no filesystem work during a skill search;
- proposition extraction bounds and sensitive-data rejection;
- BM25 query expansion, multiple HNSW variants, result collapsing, RRF, and
  deterministic `created_at` decay;
- Library unavailability degrading workspace startup without corrupting the
  workspace Session Store.
