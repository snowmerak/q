# Workspace instructions

q reads repository-local `AGENTS.md` files as bounded developer instructions.
This is separate from Agent Skills: `AGENTS.md` is loaded automatically by
location, while skills remain indexed and retrieved on demand.

## Loading and precedence

At session start, q looks for `<workspace>/AGENTS.md`. A missing file is a
normal no-op. The instruction is used by interactive chat and ACP sessions,
and root guidance is also added to subagent model requests.

When the primary agent proposes a tool call with a structured path argument,
q checks each directory between the workspace root and that path for a nested
`AGENTS.md`. Applicable files are ordered from the root toward the target, so a
deeper file overrides conflicting shallower repository guidance for its own
directory tree.

If a newly applicable nested file is found, q does not execute that batch of
tool calls. It adds the instructions, returns a tool error asking the model to
review them, and lets the model retry any still-appropriate calls. Repository
instructions are kept in the leading system/developer block, including after
context compaction. OpenAI-compatible routes that require a single initial
instruction combine that block through q's existing compatibility handling.

Subagents receive the root file before their first model request. Nested files
become available to subsequent subagent model rounds when earlier structured
tool calls identify their paths.

Repository guidance remains subordinate by contract to the system contract,
q's built-in developer instructions, and the user's explicit request.

## Path detection and safety

q recognizes path-bearing JSON fields such as `path`, `paths`, `file`,
`directory`, `source`, `destination`, and `working_directory`. It does not try
to extract paths from shell command strings. Consequently, a nested file that
is reachable only through an opaque command is not discovered automatically.

Each `AGENTS.md` is limited to 64 KiB and must be UTF-8 text without NUL bytes.
Oversized valid text is truncated with a marker. Files that are unreadable,
invalid, non-regular, or resolve through a symbolic link outside the workspace
are ignored. These checks bound repository-controlled prompt input; they are
not an operating-system sandbox.
