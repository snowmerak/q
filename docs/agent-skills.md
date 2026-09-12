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
- `payload`: source kind, `SKILL.md` content digest, Git commit when available,
  compatibility, license, metadata, and informational `allowed-tools`

The record contains no `SKILL.md` body. `search_skills` performs one bounded
Library query for global scope and one workspace Bleve query for workspace
scope, then merges the results inside the existing MCP tool. A workspace
definition shadows a same-named global result. `get_skill` routes the selected
ID to the Library or local registry and always returns the complete resource
text directly in its `content` field, whether or not Loom is available. It does
not create a Loom artifact. There is no separate Library MCP tool.

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

The indexes are derived projections. For a skill inside a Git work tree, q
records the checked-out `HEAD` commit in addition to the `SKILL.md` content
digest. The Library reconciles global roots when
the leader starts, after explicit reload, and after managed global Git
operations. The workspace reconciles workspace roots at workspace startup and
its explicit management points. Reconciliation compares both values: unchanged
records are not saved or reindexed, while a changed `SKILL.md` digest or Git
commit causes the skill record to be reindexed. Added and deleted skills are
also applied. Git detection is best-effort, so a non-Git skill, an unborn
repository, or an unavailable Git executable leaves the commit empty without
blocking discovery. Search only queries the existing projections and never
scans directories or parses YAML. External filesystem changes become visible
after explicit reload or Library/workspace restart.

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
