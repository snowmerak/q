# LSP integration

## Scope and configuration

q separates trusted language-server commands from workspace project layout.

- `~/.q/config.yaml` owns global server profiles and the default profile for
  each language. A profile contains an ID, supported languages, command,
  arguments, and enabled state.
- `<workspace>/.q/lsp.json` owns workspace-relative project roots. Each root
  contains a language, optional global profile override, discovery source, and
  enabled state. Workspace configuration cannot introduce executable commands.
- `/lsp` and `q lsp` edit both scopes. Changes remain drafts until `Ctrl+S`.

Project discovery recognizes Go workspaces/modules, Rust workspaces/crates,
TypeScript and JavaScript config roots, and Python `pyproject.toml` roots. It
honors `.qignore` and skips q metadata plus common dependency and build
directories. Server discovery checks `PATH` for a bounded registry of known
executables and never runs a candidate merely to detect it.

Language names are case-insensitive at input and persistence boundaries.
`Go`, `GO`, and `go` are stored and routed as `go`.

## Session model and routing

One LSP session is created lazily for each enabled `(project root, language)`
pair. The first tool call routed to a root starts its configured server,
performs `initialize` / `initialized`, and keeps the process for subsequent
calls. q shuts sessions down with `shutdown` / `exit` when its tool runtime
closes.

File-scoped calls select the deepest configured root containing the file. If
roots at the same depth support different languages, an explicit language wins,
then the file extension is used. An unresolved tie is an error rather than an
arbitrary selection. Workspace-symbol search with a path is routed to one
session; without a path it fans out to all enabled, resolved roots, then
deduplicates and bounds the combined results.

## Document synchronization and positions

Before a file-scoped query, q reads the file from disk. A session receives
`textDocument/didOpen` the first time and a full-content
`textDocument/didChange` whenever the disk content changes. This updates only
the server's in-memory document view; it does not write the workspace.
`didClose` is sent during normal session shutdown.

MCP inputs use 1-based line and column values. LSP uses zero-based positions;
q converts inputs to the negotiated encoding. The initial implementation
advertises UTF-16, the LSP default, to avoid ambiguous server behavior. Tool
results use workspace-relative paths and 1-based UTF-16 line/column positions.

## Read-only MCP surface

The initial MCP surface is deliberately query-only:

- `lsp_status`
- `lsp_diagnostics`
- `lsp_hover`
- `lsp_definition`
- `lsp_references`
- `lsp_document_symbols`
- `lsp_workspace_symbols`

`textDocument/documentSymbol` returns the hierarchical structure of one file;
q flattens it while retaining container names. `workspace/symbol` requires a
non-empty query because empty-query behavior varies across servers.

These tools are available to the main agent, Scout, Planner review, and Coder.
Their results pass through the existing Loom capture boundary like other MCP
results. Griller retains its existing delegation model and the isolated commit
agent retains no LSP access.

## Read-only enforcement

The client accepts only the non-mutating server-to-client requests needed for
ordinary operation, such as configuration lookup, workspace-folder lookup,
dynamic capability registration, and progress creation.
`workspace/applyEdit` is rejected explicitly. The initial MCP surface does not
send `workspace/executeCommand`, rename, formatting, code-action execution, or
any other edit-producing request.

Future mutation features must be separate approval-aware tools. Returned
`WorkspaceEdit` values must never be applied implicitly, and file create,
rename, and delete operations require the same workspace confinement and user
approval rules as q's existing filesystem tools.

## Diagnostics

Diagnostics arrive through `textDocument/publishDiagnostics` notifications and
are cached per session and document URI, including an empty published set.
`lsp_diagnostics` synchronizes the requested file and briefly waits for a newer
publication when appropriate. The cache is session-local and is not a source of
truth after the session closes.

## Failure behavior

An invalid or unresolved root is reported by `lsp_status` and rejected by
file-scoped tools. A failed process start or initialize affects only that
session. Later calls may retry a failed lazy start; healthy sessions for other
roots remain available. Configuration changes made in the TUI are persisted
but do not mutate an already running tool runtime until it is rebuilt.
