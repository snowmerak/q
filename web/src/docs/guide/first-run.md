---
title: First run
description: Configure the first model, send a request, and inspect what q changed.
sectionLabel: Guide
toc:
  - id: open-the-workspace
    label: Open the workspace
  - id: configure-a-model
    label: Configure a model
  - id: send-the-first-request
    label: Send the first request
  - id: review-the-result
    label: Review the result
---

## Open the workspace

Start q from the project directory you want it to operate on. The active workspace anchors file tools, discovery, session storage, workspace instructions, skills, and model overrides.

```powershell
cd C:\work\my-project
q
```

Starting q in a repository subdirectory does not automatically widen the file-tool boundary to the Git root. Repository-level portable Agent Skills can still be discovered from the nearest Git root.

## Configure a model

First launch opens provider setup. Add a provider, select a model, and assign it to the default role. Prefer an environment variable for the provider API key instead of storing a credential inline.

You can reopen model assignment at any time with `/model` and provider configuration with `/gateway`.

## Send the first request

Begin with a concrete repository question so you can see how q gathers evidence:

```text
Explain how this project starts and identify the main runtime components.
```

For a change that deserves an approved plan, use:

```text
/plan add a health endpoint and cover it with tests
```

`Ctrl+C` interrupts an active turn. `Ctrl+H` opens the complete key reference without discarding the current screen.

## Review the result

Run `/changes` to browse staged, unstaged, and untracked repository changes. The view represents the whole repository, including changes that existed before the current turn.

When the diff is ready, `/commit` generates a Conventional Commit or split-commit proposal. Nothing is committed until you confirm it in the review screen.
