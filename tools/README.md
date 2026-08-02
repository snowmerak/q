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
- `write_file`: create or explicitly overwrite a complete file
- `list_directory`, `create_directory`
- `move_path`, `copy_path`, `remove_path`

Every path is relative to the configured workspace unless it is an absolute
path already inside that workspace. Lexical `..` escapes and symlink escapes
are rejected. Hashline edits should always use anchors from the latest
`read_file` result.
