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
