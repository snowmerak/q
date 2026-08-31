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
- `search_text`: description plus tags, full-text indexed
- `tags`: exact tag filter
- `scope`: `global` or `project`, exact filter but not full-text search input;
  `project` is the persisted compatibility value exposed as `workspace` by
  commands and tools
- `location`: absolute checkout directory, stored in the source JSON record and
  not indexed
- `payload`: source kind, content digest, compatibility, license, metadata, and
  informational `allowed-tools`

The record contains no `SKILL.md` body. `search_skills` performs one bounded
Library query for global scope and one workspace Bleve query for workspace
scope, then merges the results inside the existing MCP tool. A workspace
definition shadows a same-named global result. `get_skill` routes the selected ID to the
Library or local registry, then stores the returned bytes as an immutable
workspace `agent-skill` Loom artifact. There is no separate Library MCP tool.

Main chat and Griller receive `search_skills` and `get_skill`; Scout receives
the same tools in its non-mutating investigation allowlist and is the preferred repository
research role for skill discovery. Planner, Coder, and commit sessions do not
receive them.

## Discovery and precedence

From lowest to highest precedence:

```text
~/.agents/skills
~/.q/skills
<workspace>/.agents/skills
<workspace>/.q/skills
```

Each direct child is one skill and must contain `SKILL.md`. The later valid
definition wins when names collide. The `/skills` management catalog retains
all valid entries so a shadowed global checkout can still be pulled or removed;
each scope keeps its own projection, and merged search suppresses a same-named
global result when a workspace definition exists. Validation and shadowing notes
remain visible in the manager.

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

The indexes are derived projections. The Library reconciles global roots when
the leader starts, after explicit reload, and after managed global Git
operations. The workspace reconciles workspace roots at workspace startup and
its explicit management points. Reconciliation compares content digests:
unchanged records are not saved or reindexed, while added, changed, and deleted
skills are applied. Search only queries the existing projections and never
scans directories or parses YAML. External filesystem changes become visible
after explicit reload or Library/workspace restart.

## Capability boundary

Resource paths cannot escape the resolved skill directory through `..` or
symlinks, and individual files are size bounded. The experimental
`allowed-tools` field is informational and cannot expand a role's tool set.
Skill content returned by `get_skill` passes through Loom and remains subject
to normal artifact retention and garbage collection.
