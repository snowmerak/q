---
title: First run
description: Start Studio, configure a model, create a repository session, and review the result.
sectionLabel: Guide
toc:
  - id: start-studio
    label: Start Studio
  - id: configure-a-model
    label: Configure a model
  - id: create-a-session
    label: Create a session
  - id: send-the-first-request
    label: Send the first request
  - id: review-the-result
    label: Review the result
---

## Start Studio

Start the embedded local web application. It can manage sessions in any repository, regardless of the directory where you launch it.

```powershell
q studio
```

Studio prints its loopback URL and opens it in your browser. Keep that q process running while you use the interface.

## Configure a model

Open **Settings → Providers**, add a Gateway provider, and enter its endpoint and API key environment variable. Then open **Settings → Models** and assign a discovered model to **Default**. Provider and model changes save automatically.

Use **Settings → System One** only when you want a separate typed-decision provider for tasks such as Agent Skill relevance checks.

## Create a session

Open **Sessions** and choose **Add session**. Select the repository directory with the folder browser, then choose an existing root session or create a new one. Studio remembers the repository location for that root session.

Starting Studio in a repository subdirectory does not widen the file boundary. Each session runs in the exact workspace directory registered for it.

## Send the first request

Begin with a concrete repository question so you can see how q gathers evidence:

```text
Explain how this project starts and identify the main runtime components.
```

For a change that benefits from a specialist, ask the main agent to delegate or explicitly name an occupational role:

```text
Ask the senior developer to add a health endpoint and verify it.
```

Child agents appear below their parent in the session tree. Select a child to inspect its own transcript and tool activity.

## Review the result

Open **Changes** to browse staged, unstaged, renamed, and untracked repository changes. Select a file for its highlighted diff.

When the diff is ready, start commit review to generate a Conventional Commit or split-commit proposal. Nothing is committed until you execute the selected proposal.
