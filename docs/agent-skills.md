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
- `search_text`: tags, full-text indexed
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
scope, then merges the results inside the existing MCP tool. If both queries
return the same name, the workspace hit is retained, and `total` is recomputed
from the merged, de-duplicated result set before the requested limit is applied.
Lexical ranking weights the skill name highest, then tags, then description.
`get_skill` routes the selected ID to the Library or local registry and always
returns the complete resource text directly in its `content` field, whether or
not Loom is available. It does not create a Loom artifact. There is no separate
Library MCP tool.

Main chat and Griller receive `search_skills` and `get_skill`; Scout receives
the same tools in its non-mutating investigation allowlist and is the preferred repository
research role for skill discovery. Planner, Coder, and commit sessions do not
receive them.

## Contextual discovery

When a role exposes both `search_skills` and `get_skill`, q performs host-side
candidate retrieval at the points where new task information becomes
available:

- a new user turn, using the user text and any active task objective and
  completion criteria;
- a successful `task_start`, using its objective and completion criteria;
- an `ask_to_user` answer, using the question and context together with the
  selected choice label and description or the free-form answer.

The normalized query is limited to 4,000 runes and requests at most eight
search hits. q adds at most four candidates after removing skill IDs already
hinted or loaded in the current context. Candidate descriptions are limited to
600 runes; each candidate retains at most twelve tags of at most 80 runes each.
Previously seen IDs are recovered from earlier contextual hints,
`task_start`/`ask_to_user` tool results, and successful `get_skill` results.

Only candidate metadata is injected. It is explicitly marked as
non-instructional, and the model must call `get_skill` with an exact candidate
ID before following that skill. If no candidate applies, or later work reveals
a different information need, the stable agent instructions continue to direct
the model to call `search_skills` itself. Automatic retrieval errors are
non-blocking and leave that model-driven fallback available.

For a new user turn, the hint is appended to the current model-facing user
message before its first provider request. The visible transcript retains the
original user text. Hints found after `task_start` or `ask_to_user` are fields
inside those new tool results. No path inserts a dynamic developer message or
rewrites an earlier request prefix. This append-only placement preserves the
existing prefix for provider prompt caching; an actual cache hit still depends
on provider routing, retention, and model support. Codex App Server routes keep
the same conversation/thread ID across ordinary follow-up turns and delegated
tool callbacks, and contextual hinting does not reset that ID.

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
each scope keeps its own projection, and merged search suppresses same-named
hits when both appear in the bounded result sets. Validation and shadowing notes
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
also applied. When an embedding model is configured, active skill metadata is
embedded and searched through the rebuildable HNSW index together with BM25;
without one, search remains BM25-only. Assigning a new model reconfigures the
vector index and backfills active skills for that model. Git detection is
best-effort, so a non-Git skill, an unborn repository, or an unavailable Git
executable leaves the commit empty without blocking discovery. Search only
queries the existing projections and never scans directories or parses YAML.
External filesystem changes become visible after explicit reload or
Library/workspace restart.

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
