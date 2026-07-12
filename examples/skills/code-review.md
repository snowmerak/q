---
name: code-review
description: >
  Review the working tree against a stated objective. Collect only material
  issues and structure severity, evidence, acceptance criteria, and plan.
  review, quality, audit.
tags: [review, quality, audit, go]
---

# Structured Code Review

Review the current working tree with the same standards as a workflow `review` step.

## Goal

Review the objective from Additional instructions (or, if none, “quality and
safety of the current changes”) using real files and tests as evidence.

## Steps

1. Identify the change surface and related test/build commands.
2. Keep only **material** issues: behavior bugs, races, data loss, security, API contract breaks.
3. Drop pure style/taste nits unless they likely cause bugs.
4. If nothing material remains, report done.

## Constraints

- Do not modify code. Observe and report only.
- Do not invent issues from speculation. Put file/symbol/symptom evidence in `evidence`.

## Output format

Use this structure (field names compatible with workflow structured review):

```text
done: true|false
summary: <one-paragraph summary>

issues:
  - id: <short-kebab-id>
    severity: low|medium|high
    description: <what is wrong>
    evidence: <file, behavior, or log evidence>
    acceptance_criteria: <when the fix counts as done>

plan:
  - issue_id: <id above>
    action: <concrete work for the improve step>
```

Rules:

- If `done=true`, leave issues and plan empty.
- If `done=false`, include at least one issue and a plan item for every issue.
