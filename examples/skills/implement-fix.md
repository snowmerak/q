---
name: implement-fix
description: >
  Apply a structured review (issues/plan) to the working tree and run focused
  tests. improve, implement, fix, apply review.
tags: [improve, implement, fix]
---

# Implement From Review

Turn a review payload into code changes, similar to a workflow `improve` step.

## Inputs

1. Objective from Additional instructions
2. Prefer a recent review (issues / plan), pasted by the user or present in session context

## Steps

1. Order plan items by issue severity and dependencies.
2. Implement each action with minimal diffs. Skip unrelated refactors.
3. Run related tests or a narrow verification command.
4. If verification fails, fix and re-run.
5. Summarize the outcome.

## Constraints

- Do not commit, push, or force branch operations unless the user asks.
- Do not expand “improvements” far beyond the review.
- Never print secrets or credentials.

## Output

- Plan items applied
- Summary of changed files
- Verification commands and results
- Remaining risks or follow-ups
