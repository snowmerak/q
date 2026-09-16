---
title: Agent Skills
description: Give q portable instructions that are discovered when the current task makes them relevant.
sectionLabel: Guide
toc:
  - id: how-retrieval-works
    label: How retrieval works
  - id: discovery-locations
    label: Discovery locations
  - id: write-a-skill
    label: Write a skill
  - id: manage-skills
    label: Manage skills
  - id: refresh-and-indexing
    label: Refresh and indexing
---

## How retrieval works

q implements the portable [`SKILL.md` Agent Skills format](https://agentskills.io/specification) as an on-demand retrieval layer. The full catalog and skill bodies stay out of the base prompt.

On a new user turn, `task_start`, or an answer returned through `ask_to_user`, q can search skill metadata and add up to four previously unseen candidate hints. A hint is not an instruction. The model must call `get_skill` before following the skill.

Roles with skill tools may also call `search_skills` later when new information shows that more guidance is needed.

Lexical ranking weights the skill name above tags and description. When embeddings are configured, q fuses BM25 and vector candidates. If a semantic candidate is confident while its lexical score is only noise, q can omit that weak BM25 contribution; this prevents a language mismatch from burying a strong semantic match. When semantic confidence is not high enough, the complete lexical branch remains available.

## Discovery locations

q reads skills in increasing precedence from:

```text
~/.agents/skills/
~/.q/skills/
<nearest-git-root>/.agents/skills/
<workspace>/.agents/skills/
<workspace>/.q/skills/
```

The nearest Git-root location is used only when it differs from the active workspace. It makes portable repository skills visible when q starts in a subdirectory without widening the workspace file-tool jail.

When the same skill name appears in both bounded result sets, the workspace definition wins.

## Write a skill

Create a directory containing `SKILL.md`:

```markdown
---
name: release-check
description: Verify a release candidate before publishing it.
---

# Release check

Read the release manifest, run the documented verification, and report evidence.
```

The frontmatter `name` is canonical but does not have to match the directory name. `description` is optional. Keep the body scoped to work where the instructions materially change the result.

## Manage skills

Open `/skills` or run `q skills`. The two panels correspond to q-managed locations:

```text
GLOBAL    -> ~/.q/skills/<skill-name>
WORKSPACE -> <workspace>/.q/skills/<skill-name>
```

Use left/right or Tab to switch scope, Up/Down to select, `A` to clone a Git repository, `U` to fast-forward an installed skill, `D` to remove after confirmation, and `R` to rediscover all roots and reconcile the indexes.

Skills found under `.agents/skills` appear in the catalog but are read-only because q does not own those directories. A managed repository must contain `SKILL.md` at its root; its destination name comes from validated frontmatter rather than the repository URL.

## Refresh and indexing

Without an embedding model, retrieval uses BM25 metadata search. With one assigned through `/model`, q combines BM25 and HNSW vector results. Assigning a new embedding model rebuilds and backfills the vector projection.

While q is running, skill use reconciles workspace skill roots when the previous check is at least 30 seconds old. Use `/skills` to manage q-owned skills and request an explicit reindex.
