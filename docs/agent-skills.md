# Agent Skills Authoring Guide

An **Agent Skill** in `q` is a reusable SOP (standard operating procedure) prompt.  
Define it as a single Markdown file, run it with a slash command (`/skill-name`), or let the agent discover it via the `search_skills` and `get_skill` tools.

Skills and workflows (YAML review → improve → verify loops) serve different roles.

| | Agent Skill | Workflow |
|---|---|---|
| Format | Markdown + YAML frontmatter | YAML (`version`, `loop`) |
| Location | `.skills/`, `~/.config/q/skills/` | `.q/workflows/`, global workflows dir |
| How to run | `/name [args]` or agent tools | `q workflow run …` |
| Purpose | Inject one-shot instructions / procedure | Checkpointed multi-step loop |
| State | Prompt inserted into session messages | Iterations stored under `workflow-runs` |

Skills tell the agent *how* to work. Workflows decide *how to loop* review, implementation, and verification across providers. Use both together when useful (for example, encode review standards in a skill, and run the same standards in a workflow loop).

---

## 1. Load paths and rules

On startup, `LoadSkills()` reads `*.md` files in this order:

1. **Global**: `~/.config/q/skills/`  
   - Windows example: `C:\Users\<you>\.config\q\skills\`
2. **Project (local)**: `.skills/` under the current working directory

If two skills share the same `name`, the **later-loaded local skill overrides the global one.**  
Subdirectories are not scanned. Only `*.md` files directly in the skills directory are loaded.

```
project/
  .skills/
    code-review.md          ✅ loaded
    commit-message.md       ✅ loaded
    nested/foo.md           ❌ ignored
```

---

## 2. File format

### 2.1 Recommended: frontmatter + body

```markdown
---
name: code-review
description: Structure issues, evidence, and a fix plan from the working-tree diff.
tags: [review, quality, go]
---

# Code Review

## Goal
Review the objective against the current working tree and tests.

## Steps
1. List changed files and related tests.
2. Report only material issues (skip style-only nits).
3. Attach evidence (file/behavior) and acceptance criteria to each issue.

## Output
- Summary
- Issue list (severity, description, evidence, acceptance)
- Fix action per issue
```

### 2.2 Frontmatter fields

| Field | Required | Description |
|---|---|---|
| `name` | Recommended | Slash-command name. Normalized to lowercase. Falls back to the filename without extension |
| `description` | Recommended | Shown in `/skills` and used for search. Keep it short and specific |
| `tags` | Optional | String array for `search_skills` matching |
| `prompt` | Optional | If set in frontmatter, used instead of the body. Prefer the body in most cases |

The body is everything after the closing `---` of the frontmatter. It is trimmed and stored as `Prompt`.

### 2.3 Files without frontmatter

```markdown
The entire file becomes the prompt.
The filename (e.g. quick-fix.md) becomes the name.
description is fixed to "Custom skill loaded from file".
```

Prefer frontmatter for searchable listings and clearer metadata.

### 2.4 Naming rules

- Names are lowercased on load, so `/Code-Review` and `/code-review` are the same
- The first token after `/` is the skill name: `/code-review fix panic in session save`
- Avoid spaces and special characters; prefer `code-review`, `go-test-fix`

---

## 3. How skills run

### 3.1 User: slash commands

```text
/skills
/code-review
/code-review focus on race conditions in the session save path
```

Behavior:

1. Append `skill.Prompt` as a user message in the session
2. If extra arguments are present, append them as:

```text
<skill.Prompt>

Additional instructions:
focus on race conditions in the session save path
```

3. Run the normal agent loop (`agent.Run`) with tools available

### 3.2 Agent: tools

| Tool | Role |
|---|---|
| `search_skills` | Substring search over name / description / prompt / tags → skill metadata JSON |
| `get_skill` | Fetch the full skill (including prompt) by exact `name` |

The agent typically searches first, then calls `get_skill` for the body.  
Put searchable keywords in **description** and **tags**.

---

## 4. Authoring patterns

Treat the skill body as an **execution brief for the agent**, not an essay. Spell out steps, constraints, and output shape.

### 4.1 Structure template

```markdown
---
name: my-skill
description: One line on what it does and when. Keywords: foo, bar
tags: [foo, bar]
---

# Title

## When to use
- …

## Inputs
- Additional instructions from the user
- Working tree / test results as needed

## Steps
1. …
2. …
3. …

## Constraints
- What not to do (e.g. no commits, no unrelated refactors)
- Allowed tools / command scope

## Done when
- …

## Output format
- …
```

### 4.2 Writing a good description

- Cover **what** and **when** in one or two sentences
- Include words the agent will search for: `review`, `test`, `commit`, `refactor`, etc.
- Weak: `"utility"`  
  Strong: `"Review Go package changes with go test and race-safety focus."`

### 4.3 Aligning with workflows

The workflow structured-review schema looks like this:

```json
{
  "done": true,
  "summary": "…",
  "issues": [
    {
      "id": "…",
      "severity": "low|medium|high",
      "description": "…",
      "evidence": "…",
      "acceptance_criteria": "…"
    }
  ],
  "plan": [
    { "issue_id": "…", "action": "…" }
  ]
}
```

Rules:

- `done=true` → `issues` and `plan` must be empty
- `done=false` → at least one issue; every issue needs a plan item
- All issue fields must be non-empty; severity is only `low` / `medium` / `high`

Use a skill to apply the **same review habits** in an interactive session. For automated loops, copy or adapt those standards into the workflow YAML `review.prompt` / `improve.prompt`.

Workflow template variables (workflow only; **not** expanded in skill files):

| Variable | Meaning |
|---|---|
| `{{ input }}` | Objective string from `q workflow run …` |
| `{{ iteration }}` | Current iteration number |
| `{{ review }}` | Previous structured review JSON |
| `{{ improve }}` | Improve-step output |
| `{{ verify }}` | Verify command stdout/stderr |

Skill bodies do **not** expand `{{ … }}`.  
Skill arguments are passed only via the `Additional instructions:` block.

---

## 5. Examples

Copy the repository examples into a project or global skills directory:

```bash
# Project-local
mkdir .skills
cp examples/skills/*.md .skills/

# Global (Linux/macOS/Git Bash)
mkdir -p ~/.config/q/skills
cp examples/skills/*.md ~/.config/q/skills/
```

PowerShell:

```powershell
New-Item -ItemType Directory -Force -Path .skills | Out-Null
Copy-Item examples\skills\*.md .skills\

New-Item -ItemType Directory -Force -Path "$HOME\.config\q\skills" | Out-Null
Copy-Item examples\skills\*.md "$HOME\.config\q\skills\"
```

Included examples:

| File | Role |
|---|---|
| `code-review.md` | Structured review similar to the workflow `review` step |
| `implement-fix.md` | Improve-style implementation from a review payload |
| `verify-go.md` | `go test`-oriented verification procedure |

In chat:

```text
/code-review objective is safer atomic session saves
```

As a workflow loop with the same objective:

```bash
q workflow run examples/workflows/improve-until-clean.yaml "Safer atomic session saves"
```

---

## 6. Checklist

When adding a skill:

- [ ] Save under `.skills/<name>.md` or `~/.config/q/skills/<name>.md`
- [ ] `name` is lowercase/hyphenated and unique
- [ ] `description` includes searchable verbs and domain terms
- [ ] `tags` lists short related keywords
- [ ] Body has steps / constraints / done criteria / output format
- [ ] Restart `q` and confirm the skill appears under `/skills`
- [ ] Run `/<name> …` once and verify prompt + additional instructions combine as expected

On parse failure, that file is skipped with a warning. Check matching `---` lines and YAML syntax.

---

## 7. Minimal skeleton

```markdown
---
name: my-task
description: One-line TODO. Include trigger keywords.
tags: [todo]
---

# My Task

## Steps
1. Inspect the current state.
2. Make only the required changes.
3. Run verification commands.

## Constraints
- Do not refactor beyond the request.
- Commit or push only when the user asks.

## Output
- Summary of work done
- Verification results
- Remaining risks
```

Then:

```text
/skills
/my-task concrete extra instructions
```
