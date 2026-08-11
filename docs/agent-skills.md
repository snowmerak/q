# Agent Skills

q implements the portable [Agent Skills specification](https://agentskills.io/specification)
as a disk-indexed retrieval layer. It never injects the full skill catalog into
every model request.

## Storage and retrieval

Each active discovered skill is projected into the workspace Session Store as
a `skill` record:

- `summary`: skill title/name, full-text indexed
- `content`: description, full-text indexed
- `search_text`: description plus tags, full-text indexed
- `tags`: exact tag filter
- `scope`: `global` or `project`, exact filter but not full-text search input
- `location`: absolute checkout directory, stored in the source JSON record and
  not indexed
- `payload`: source kind, content digest, compatibility, license, metadata, and
  informational `allowed-tools`

The record contains no `SKILL.md` body. `search_skills` performs a bounded
Bleve query and returns only IDs and metadata. `get_skill` resolves one ID,
reads `SKILL.md` or an optional relative resource from disk, and stores its
bytes as an immutable `agent-skill` Loom artifact. The response contains the
artifact reference rather than the file body.

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
only the winning definition is projected into the search index. Validation and
shadowing notes remain visible in the manager.

## Git management

q manages Git checkouts in its native roots. Open `/skills`; the two panels map
to these locations:

```text
GLOBAL   -> ~/.q/skills/<skill-name>
SESSION  -> <workspace>/.q/skills/<skill-name>
```

Use `Tab` or the left/right arrows to focus a panel, the up/down arrows to
select, `A` to enter a repository URL and clone it, `U` to pull with
`git pull --ff-only`, `D` to remove after confirmation, and `R` to rediscover
all roots and reconcile the Bleve index. `.agents/skills` entries appear in
their corresponding panel but are read-only because q does not own them.

The cloned repository must have `SKILL.md` at its root. The destination name is
taken from validated frontmatter, not from the repository URL. Updates refuse
non-fast-forward integration.

The index is a derived projection. q reconciles it at startup, on interactive
reload, after managed Git operations, and immediately before every skill
search. Consequently additions, modifications, and deletions made by another
Git or filesystem process are reflected on the next search. Stale `skill`
records are deleted; conversation records remain untouched.

## Capability boundary

Resource paths cannot escape the resolved skill directory through `..` or
symlinks, and individual files are size bounded. The experimental
`allowed-tools` field is informational and cannot expand a role's tool set.
Skill content returned by `get_skill` passes through Loom and remains subject
to normal artifact retention and garbage collection.
