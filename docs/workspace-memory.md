# Workspace Memory

Workspace Memory is q's user-level owner for durable, workspace-scoped archive
storage. It applies the same leader-or-client operating pattern as the Global
Library, but exposes a purpose-built record and search API instead of reusing
the Library interface.

The service solves one concrete ownership problem: multiple q processes must
not independently open the same Bleve and HNSW files. A single local service
therefore owns those handles and multiplexes requests by canonical workspace
root.

## Running the service

Ordinary `q`, `q acp`, and `q-mcp` processes ensure Workspace Memory is
available during startup. If no compatible leader is running, one process wins
the user-level service lock and embeds the HTTP server. Other processes connect
to that leader.

Run a dedicated foreground participant when the service should outlive a TUI,
ACP, or MCP client:

```powershell
q memory
```

`q memory` uses the same election path. It hosts the server if it becomes
leader, connects as a follower if another compatible leader already exists,
and can take over after that leader exits.

## Endpoint and authentication

The default endpoint is:

```text
http://127.0.0.1:17892/v1
```

Workspace Memory accepts loopback IP addresses only. Its endpoint configuration
is read from `~/.q/workspace-memory.json`; when that file is absent, q uses
`127.0.0.1:17892`. The service uses a fixed rendezvous port rather than an
ephemeral port so independently started q processes can find it.

All operations except the compatibility health probe require a bearer token.
q generates a 32-byte random token on first use, stores its hexadecimal form in
`~/.q/workspace-memory.token`, and writes the file with mode `0600` on POSIX.
The token is private to the local q installation and is not shared with Gateway
or Library authentication. Windows file modes do not configure ACLs.

Concurrent first-start token creation is serialized with
`~/.q/workspace-memory-token.lock`. The winner writes a temporary file, syncs
it, and atomically replaces the credential path; followers then load that one
credential instead of generating divergent tokens.

The user-level leader election lock is `~/.q/workspace-memory.lock`. Like q's
other lock files, it contains owner diagnostics, but exclusivity comes from the
open OS file lock rather than the file's mere existence.

## Workspace multiplexing and leases

Opening a workspace resolves the supplied root to an absolute path, evaluates
symlinks, and cleans the result. It also compares open roots with `os.SameFile`,
so Windows casing or alias paths that identify the same filesystem directory
are merged without relying on string lowercasing. The service maps that
canonical filesystem identity to one open Session Store and returns an
independent lease to each client.

```text
q / q acp / q-mcp clients
          |
          | loopback HTTP + bearer token
          v
Workspace Memory leader
          |
          +-- canonical root A -> one Store -> records + Bleve + HNSW
          |                         leases: client 1, client 2
          |
          `-- canonical root B -> one Store -> records + Bleve + HNSW
                                    lease: client 3
```

The default lease lifetime is two minutes. The client renews in the background,
and a successful workspace operation also extends the lease. Explicit close is
idempotent. After the final lease is released or expires, the server closes the
Store and releases that root's `.q/workspace-memory.lock`. If the service exits,
it closes all open Stores and their locks.

All leases on one open root must agree on the embedding model and dimensions.
Incompatible vector configuration is rejected rather than allowing two clients
to reinterpret the same semantic index differently. A client may change the
active vector configuration only while it holds the workspace's sole lease;
with multiple leases, the service rejects the change until the others close.

## Storage and API ownership

Workspace Memory owns these paths while a root is leased:

- `.q/data/records/`, the durable JSON source records;
- `.q/index/bleve/`, the derived lexical index;
- `.q/index/vectors.hnsw`, `.q/index/vectors.ids.json`, and
  `.q/index/state.json`, the derived semantic index and rebuild state;
- `.q/workspace-memory.lock`, the service's per-root ownership lock.

The authenticated HTTP API covers workspace open/lease renewal/release, record
save and batch save, get, delete, search, vector configuration, and a durability
barrier. Store writes are synchronous, so the current flush endpoint is an
explicit no-op barrier. Source records keep their existing durability rule:
an indexing failure may be returned after the JSON record has already been
saved. The client assigns a stable record ID before the first request, so a
mutation retried after response loss overwrites that same record instead of
creating a duplicate. The prepared record is still returned with a structured
indexing error so the writer can retain that ID and retry safely.

Embedding provider calls remain in the client process. q prepares record
embeddings and semantic query vectors, then sends those values to Workspace
Memory; the server owns their durable storage and HNSW indexing, not the model
provider connection.

## What remains process-local

This first integration intentionally moves only records and their derived
indexes. The following remain owned by the TUI, ACP, or `q-mcp` process:

- `.q/session.json`, including the current transcript projection, run identity,
  task lifecycle, and Thinker queue;
- `.q/plan-execution.json`, the approved execution checkpoint;
- `.q/loom/` and its garbage collection;
- LSP managers and `.q/lsp.json`;
- workspace file tools and all project-file mutations.

Workspace Memory does not add write leases or path-level conflict detection for
project files. If two sessions edit overlapping files, coordination is a user
and workflow concern rather than a memory-service responsibility.

## Transitional workspace lock

The existing `.q/workspace.lock` remains separate from
`.q/workspace-memory.lock`. TUI, ACP, and `q-mcp` startup still hold the former
for the lifetime of their local workspace runtime. This preserves the current
single-projection assumptions around `.q/session.json`,
`.q/plan-execution.json`, Loom, and related workflow state until those files are
split under session UUIDs. `q commit` and standalone settings commands that own
workspace state also retain their existing lock behavior.

Consequently, Workspace Memory's lease model is ready to share one archive
owner across clients, but it does not yet by itself enable two full TUI/ACP
runtimes to use the same workspace concurrently. Removing that remaining
restriction belongs to the later session-UUID migration.

## Separation from the Global Library

Workspace Memory and the Global Library are independent services with different
data ownership:

| Service | Durable scope |
|---|---|
| Workspace Memory | Workspace history records, project Agent Skill record projections, Bleve, and HNSW under the workspace `.q/` directory. |
| Global Library | Global Agent Skills, propositions, Library search projections, and proposition judging under `~/.q/library/`. |

In particular, `~/.q/library/proposition-jobs.sqlite` remains the Global
Library's serialized proposition-judging queue. Workspace Memory neither opens
that SQLite database nor introduces a workspace SQLite database.

## Failure and recovery behavior

- A client first probes the configured endpoint and verifies the service name
  and protocol version.
- If no leader answers, it attempts the user-level service lock and listener.
- If another process owns the lock, the client waits briefly for that leader to
  become ready rather than starting a competing server.
- An occupied port serving an incompatible protocol is reported as an error.
- A network disconnect, leader handoff, closed workspace, or expired lease
  triggers a bounded five-second automatic reopen using the same workspace and
  lease identity. The interrupted operation is retried once after reopen.
- Mutating requests carry client-assigned stable record IDs, so a save that
  reached the old leader before its response was lost remains idempotent when
  retried against the replacement leader.
- Bleve and HNSW remain derived state and can be rebuilt from the JSON records
  using the existing Session Store recovery rules.

The service boundary preserves the existing local Store error identities for
record-not-found, index-lock, lease, and indexing failures so callers can keep
their current retry and fallback behavior.
