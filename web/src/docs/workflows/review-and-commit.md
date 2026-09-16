---
title: Review and commit
description: Inspect repository-wide changes and create a reviewed Conventional Commit without leaving q.
sectionLabel: Guide
toc:
  - id: inspect-repository-changes
    label: Inspect changes
  - id: understand-the-view
    label: Understand the view
  - id: prepare-a-commit
    label: Prepare a commit
  - id: review-and-finish
    label: Review and finish
---

## Inspect repository changes

Run `/changes` to open q's read-only Git browser. It lists staged, unstaged, and untracked files for the enclosing repository.

- Up/Down or `j`/`k` selects a file.
- Enter focuses its diff; Tab switches between the file and diff panes.
- PageUp/PageDown scrolls vertically; Left/Right scrolls long lines.
- `r` reloads repository state; Escape returns to chat.

Opening this view never stages, edits, or commits a file.

## Understand the view

The browser shows the repository's current state, including changes that existed before the current q turn. It is not a per-agent audit log.

Staged and unstaged patches are shown separately even when they cancel each other relative to `HEAD`. Known text formats use syntax highlighting. Binary files show a notice, and unknown or oversized sources fall back to plain text. Each selected preview is limited to 256 KiB or 4,000 lines and is marked when partial.

## Prepare a commit

Run `/commit` from chat or start the standalone workflow in the current repository:

```powershell
q commit
```

q uses the existing index when staged changes are present. If the index is empty, it stages the current working-tree changes while excluding q-owned root `.q` metadata. It then asks the configured `commit` model for either one Conventional Commit message or a split-commit proposal.

Before creating a commit, q verifies that the captured index still matches the proposal. If repository state changed during review, regenerate or restart rather than committing a stale grouping.

## Review and finish

In the commit screen, use Up/Down to select a proposed split, `e` to edit its message, `Ctrl+S` to save an edit, and `r` to regenerate. Enter commits the approved proposal. `p` commits and pushes; pushing requires an existing upstream. Escape cancels without creating a commit.

Review each split's files and message before approval. q can propose and execute the Git operations, but it does not infer that unrelated pre-existing changes belong to the current task.
