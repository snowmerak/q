# q (Stateful AI CLI Copilot)

`q` is an interactive agent CLI that connects to a local LLM through an OpenAI-compatible API. It interprets natural-language requests, proposes shell commands, executes tools, and analyzes the results to complete tasks.

## Features

- **Agentic execution and tool calling**: The LLM can call tools such as `run_shell_command`, inspect the results, and continue working until the task is complete.
- **Automatic working-directory synchronization**: The working directory is synchronized after each command, so directory changes such as `cd` persist throughout the session.
- **Simple session management**: Unix-style commands (`ls`, `rm`, `mv`, and `cp`) manage and restore independent conversation sessions.
- **Automatic context compression**: When accumulated context reaches 85% of the configured token limit, `q` summarizes the conversation to prevent context overflow.
- **Terminal UI**: ANSI colors and status indicators make agent activity easier to follow.
- **Native agent sessions**: Each q session can own and resume separate Codex, Grok, agy, and Claude Code sessions.

## Requirements and Build

Build with Go 1.26 or later:

```bash
go build -o q.exe
```

## Usage

### 1. Start an Interactive Agent Shell

Resume the most recently used session. If no previous session exists, `q` creates a session named `default`:

```bash
q
```

Start or resume a named session:

```bash
q <session_name>
```

### 2. Manage Sessions

List saved sessions:

```bash
q ls
```

Copy a session:

```bash
q cp <source_session> <destination_session>
```

Rename a session:

```bash
q mv <old_name> <new_name>
```

Delete a session:

```bash
q rm <session_name>
```

Copied sessions do not inherit native provider session IDs. A new provider conversation is created the first time the copied q session invokes that provider.

### 3. Interactive Commands

Enter these commands directly at the interactive prompt:

- `clear` or `cls`: Clear the conversation while preserving the system prompt.
- `compact`: Compress the current conversation immediately, regardless of the automatic 85% threshold.
- `/skills`: List loaded skills.
- `/<skill_name> [args]`: Run a loaded skill.
- `/codex <prompt>`: Run a turn in this q session's Codex thread.
- `/grok <prompt>`: Run a turn in this q session's Grok session.
- `/agy <prompt>`: Run a turn in this q session's agy conversation.
- `/claude <prompt>`: Run a turn in this q session's Claude Code session.

### 3.1 Agent Skills

Skills are reusable SOP prompts stored as Markdown files. The agent can also discover them with the `search_skills` and `get_skill` tools.

Load paths (local overrides global when names collide):

- **Project**: `.skills/*.md`
- **Global**: `~/.config/q/skills/*.md`

Minimal skill file:

```markdown
---
name: code-review
description: Review the working tree against a stated objective.
tags: [review, quality]
---

# Code Review

1. Inspect relevant files and tests.
2. Report only material issues with evidence.
3. Propose concrete fix actions.
```

Usage:

```text
/skills
/code-review focus on session save races
```

Example skills that mirror the workflow review → improve → verify loop live in `examples/skills/`. Full authoring guide: [docs/agent-skills.md](docs/agent-skills.md).

### 4. Manage Configuration

Print the complete configuration as JSON:

```bash
q config
```

Read a configuration value:

```bash
q config <key>
```

Update and save a configuration value:

```bash
q config <key> <value>
```

Examples:

```bash
q config model qwen2.5-7b
q config timeout 300
```

### 5. Use Codex app-server

Run a one-shot task through an installed and authenticated Codex CLI:

```bash
q codex "Run the tests in this project and explain any failures."
```

Inside an interactive q session, use the Codex thread associated with that session:

```text
/codex Run the tests in this project.
```

`q` starts `codex app-server --stdio` as a child process. If the current q session does not have a Codex thread, `q` creates one with `thread/start` and stores its ID. Later invocations resume the same conversation with `thread/resume`. The app-server process exits after each invocation, but the Codex thread persists.

Codex runs with the q session's working directory, the `workspace-write` sandbox, and the `never` approval policy.

### 6. Use Grok, agy, and Claude Code Sessions

Each q session independently owns a Grok session, an agy conversation, and a Claude Code session. `q` creates and stores the provider's native session ID on the first invocation and resumes it on later invocations.

Native provider session IDs are scoped to the working directory where they were created. If a q session moves to another working directory, q creates a new native provider session instead of attempting to resume an incompatible project session.

Inside an interactive session:

```text
/grok Review this code for potential problems.
/agy Continue by running the relevant tests.
/claude Review the resulting changes.
```

Outside interactive mode, provider commands use the most recently active q session:

```bash
q grok "Analyze the failing tests."
q agy "Fix the issue based on the previous analysis."
q claude "Review the current implementation."
```

The corresponding provider CLI must be installed, authenticated, and available on `PATH`.

## Workflows (step graphs)

`q` runs checkpointed **workflow graphs**: freely defined steps connected by conditional edges (including loops). There is no fixed review/improve/verify loop.

```yaml
version: 1
name: plan-work-verify
entry: plan
limits:
  max_visits: 24
steps:
  plan:
    type: provider
    provider: claude
    prompt: |
      PLAN for: {{ input }}
    output: { kind: structured, schema: plan }
  work:
    type: provider
    provider: grok
    prompt: |
      WORK using plan: {{ steps.plan }}
  verify:
    type: provider
    provider: claude
    prompt: |
      VERIFY work: {{ steps.work }}
    output: { kind: structured, schema: verdict }
edges:
  - { from: plan, to: end, when: "steps.plan.done == true" }
  - { from: plan, to: work, when: "steps.plan.done == false" }
  - { from: work, to: verify, when: always }
  - { from: verify, to: end, when: "steps.verify.pass == true" }
  - { from: verify, to: plan, when: "steps.verify.pass == false" }
sinks:
  end: { type: success }
```

Step types: `provider` (`codex` | `grok` | `claude` | `agy`) or `command` (shell). Built-in structured schemas: `plan`, `verdict`. Template context includes `{{ input }}`, `{{ visit }}`, `{{ step }}`, and `{{ steps.<id> }}` / nested fields.

```bash
q workflow run examples/workflows/plan-work-verify.yaml "Safer atomic session saves"
```

Project workflows: `.q/workflows`. Global: `%APPDATA%\q\workflows` (Windows) or `~/.config/q/workflows` (Linux/macOS). Project overrides global for the same name.

```bash
q workflow init my-flow
q workflow init shared-flow --global
q workflow get upstream-flow https://example.com/flow.yaml
q workflow ls
q workflow path
q workflow run shared-flow "objective"
q workflow runs
q workflow status <run_id>
q workflow resume <run_id>
```

Downloaded workflows must be HTTPS, pass graph validation, and be under 1 MiB. Legacy fixed-loop YAML (`loop.review` / `loop.improve`) is rejected. Design notes: [docs/workflow-dag-design.md](docs/workflow-dag-design.md), [docs/workflow-dag-schema.md](docs/workflow-dag-schema.md).

Each run writes a step-by-step work log under the session working directory:

```text
.q/workflow-logs/<run-id>.log
```

The console also prints a short preview of each step's output. Full provider transcripts and routing decisions live in that log file; durable run state (including all visit payloads) is still stored under the q config `workflow-runs` directory.

## Configuration

The configuration file is stored at:

- **Windows**: `%APPDATA%\q\config.json`
- **Linux and macOS**: `~/.config/q/config.json`

### Configuration Keys

- `"api_endpoint"` (alias: `endpoint`): OpenAI-compatible API endpoint.
- `"model"`: LLM model used by the OpenAI-compatible backend.
- `"max_context_tokens"` (alias: `tokens`): Maximum context size used to trigger automatic compression. Default: `4096`.
- `"api_timeout_seconds"` (alias: `timeout`): API request timeout in seconds. Default: `300`.
- `"last_session"`: Name of the most recently active q session.
