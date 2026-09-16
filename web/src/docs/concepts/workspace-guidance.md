---
title: Workspace guidance
description: Control repository-specific agent behavior with AGENTS.md and discovery with .qignore.
sectionLabel: Concepts
toc:
  - id: agentsmd-and-agent-skills
    label: AGENTS.md and skills
  - id: loading-and-precedence
    label: Loading and precedence
  - id: path-detection
    label: Path detection
  - id: instruction-safety
    label: Instruction safety
  - id: discovery-with-qignore
    label: Discovery with .qignore
---

## AGENTS.md and Agent Skills

Use `AGENTS.md` for repository instructions that should apply automatically by location. Use Agent Skills for reusable guidance that should be searched and loaded only when a task makes it relevant.

The workspace-root `AGENTS.md` applies to the whole q workspace. A nested file applies to its directory and descendants.

## Loading and precedence

q loads `<workspace>/AGENTS.md` when a session starts. The root guidance is available to ordinary chat, ACP sessions, and subagent model requests.

When a structured tool call identifies a path, q checks directories from the workspace root toward that target. Newly applicable nested files are inserted root-to-leaf, so the deeper `AGENTS.md` wins when repository instructions conflict inside its subtree.

If a new nested file is discovered immediately before a tool batch, q pauses that batch, adds the instruction, and returns a tool error asking the model to review it. The model can then retry only the calls that are still appropriate. Loaded instructions remain in the leading instruction block through context compaction.

Repository guidance remains subordinate to the system contract, q's built-in developer instructions, and the user's explicit request.

## Path detection

q recognizes structured JSON fields such as `path`, `paths`, `file`, `directory`, `source`, `destination`, `root`, `workdir`, and `working_directory`.

It deliberately does not parse shell command strings for paths. If a nested directory is reached only through an opaque shell command, its `AGENTS.md` is not discovered automatically before that command runs.

## Instruction safety

Each `AGENTS.md` is limited to 64 KiB and must be UTF-8 text without NUL bytes. Oversized valid text is truncated with a marker. Unreadable files, non-regular files, invalid text, and symlinks that resolve outside the workspace are ignored so repository-controlled prompt input remains bounded.

These checks constrain instruction loading. They do not sandbox shell commands or make repository instructions trusted above user and system policy.

## Discovery with .qignore

The workspace-root `.qignore` filters directory discovery and root listings. It supports comments, negation, root anchoring, directory suffixes, and `*`, `**`, and `?` wildcards. q's own `.q/` metadata is always excluded from discovery.

`.qignore` is not an access-control boundary. A model tool can still read an explicitly requested path when that path remains inside the workspace. Use operating-system permissions or isolation when a path must not be accessible at all.
