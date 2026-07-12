# Workflow DAG Design (Draft)

Status: **draft** — design only; not implemented.  
Related: [workflow-dag-schema.md](./workflow-dag-schema.md), [workflow-rewrite-plan.md](./workflow-rewrite-plan.md)

**Strategy: clean rewrite.** The fixed `loop.review / improve / verify` engine is removed entirely. There is no dual-runner, no migrate command, and no loading of legacy loop YAML. The graph document is the only workflow format (`version: 1` of the new engine).

## 1. Motivation

### 1.1 What we are replacing

The shipping engine is a **fixed three-slot loop** (legacy, to be deleted):

```text
for iteration = 1 .. max_iterations:
  review   (provider, structured: done/summary/issues/plan)
  improve  (provider, free-form text)
  verify?  (shell command only)
  if review.done → stop
```

Authors can change prompts and providers inside those slots. They **cannot**:

- define arbitrary step names or counts
- reorder steps or branch on conditions
- attach a provider to an arbitrary verify-style node
- loop only a subgraph (e.g. work → verify → work) without re-running plan
- pass arbitrary step outputs by name

That matches a single “improve until clean” product pattern, not a general workflow engine.

### 1.2 Target model

| Concept | Meaning |
|---|---|
| **Step** | A freely authored unit of work (provider turn, shell command, or future types) |
| **Workflow** | A **directed graph** of steps with **conditional edges** |
| **Loop** | Cycles in the graph, with explicit termination and safety bounds |
| **State** | Named outputs from steps, available to later steps via templates / bindings |

Target shape:

```text
steps:  free definitions (id, type, provider|command, prompts, optional schema)
graph:  edges from → to with when conditions; entry + terminal nodes
runtime: execute current node → store output → pick next edge → repeat
```

Example intent (plan → work → verify, loop on failure):

```text
        ┌─────────┐
   ┌───►│  plan   │── when: done ──────────────► (end success)
   │    └────┬────┘
   │         │ when: needs_work
   │         ▼
   │    ┌─────────┐
   │    │  work   │
   │    └────┬────┘
   │         ▼
   │    ┌─────────┐
   └────│ verify  │── when: pass ──────────────► (end success)
        └─────────┘── when: fail ──► plan (or work)
```

---

## 2. Goals and non-goals

### 2.1 Goals

1. **Free step authoring** — any number of steps; user-chosen ids; mix of providers and commands.
2. **Conditional wiring** — edges selected by predicates over step outputs and run state.
3. **Cycles / loops** — directed graphs that may contain cycles, with visit guards.
4. **Explicit data flow** — templates reference step outputs by id (`{{ steps.plan }}`, etc.).
5. **Checkpoint / resume** — durable runs under `workflow-runs`, keyed by cursor + visits.
6. **Multi-provider** — each provider step names `codex` | `grok` | `claude` | `agy`.
7. **Clean break** — delete the fixed-loop engine; reject legacy YAML with a clear error.

### 2.2 Non-goals (first ship)

- Visual editor / UI
- Parallel fan-out/fan-in — **sequential single-cursor only**
- Human-in-the-loop approval gates (later step type)
- Cross-machine distributed execution
- Full programming language in conditions (small expression subset)
- Replacing Agent Skills
- **Loading or compiling legacy `loop.review` documents**
- **Resuming old run JSON** produced by the fixed-loop engine

### 2.3 Design principles

- **Steps are pure definitions; the graph is control flow.** Don’t bury next-step logic only inside prompts.
- **Outputs are data; edges are control.** Prefer routing on structured fields (`done`, `pass`) over parsing free text.
- **Fail closed.** Ambiguous conditions, missing outputs, or cycles without a bound → error, not silent skip.
- **One cursor.** Exactly one current step at a time (simpler checkpoints).
- **One format.** Graph YAML only.

---

## 3. Conceptual model

### 3.1 Run

A **run** is an execution of a workflow definition against:

- `input` (string objective from CLI)
- `session` (q session: PWD, provider session ids)
- durable checkpoint file under `workflow-runs`

### 3.2 Step

A **step** has:

- `id` — unique name within the workflow
- `type` — how it executes (`provider`, `command`, …)
- execution fields (`provider`/`prompt` or `command`)
- optional **output contract** (JSON schema or built-in kind)
- optional local limits (`timeout`, retries)

Step types (first ship):

| type | Behavior | Output stored as |
|---|---|---|
| `provider` | Call native provider with rendered prompt; optional structured schema | text and/or parsed JSON |
| `command` | Run shell command in session PWD | combined stdout/stderr, exit code |

Future (not required for first cut): `skill`, `http`, `gate` (human approve).

### 3.3 Graph

A **graph** has:

- `entry` — step id where execution starts
- `edges[]` — directed links `from` → `to` with `when` condition
- terminal outcomes — success / failure without a next step, or explicit `end` nodes

**Note on “DAG”:** Users often say DAG for “workflow graph.” The engine allows **cycles** (loops). Strictly it is a **directed graph**, possibly cyclic.

### 3.4 Visit / step run

Each time the cursor enters a step, that is a **visit** (or step run):

- `visit_id`, `step_id`, `index` (monotonic)
- rendered prompt/command
- raw output, parsed output, status, error
- timestamps

The run’s history is an ordered list of visits. Template context can expose:

- last output of a step by id
- full history (optional, for advanced prompts)

### 3.5 Conditions

An edge is taken when its `when` predicate is true **after** the `from` step completes successfully (or, for failure edges, when it fails — see schema).

Evaluation order when multiple edges match:

1. Evaluate edges in **declaration order**
2. Take the **first** matching edge
3. If none match and the step is not marked terminal → **run error** (fail closed)

### 3.6 Termination

A run ends when:

- an edge targets a built-in sink (`end` / `success` / `fail`), or
- the current step completes and has **no outgoing edges** and `terminal: true`, or
- a global guard trips (`max_visits`, `max_duration`, …) → failed

---

## 4. Data flow

### 4.1 Template context (draft)

| Key | Description |
|---|---|
| `{{ input }}` | CLI objective string |
| `{{ visit }}` | Current visit index (1-based) |
| `{{ step }}` | Current step id |
| `{{ steps.<id> }}` | Last successful output of step `id` (string or JSON serialization) |
| `{{ steps.<id>.json }}` | Parsed object when structured |
| `{{ steps.<id>.field }}` | Convenience for top-level JSON fields (e.g. `{{ steps.plan.done }}`) |
| `{{ history }}` | Optional compact summary of prior visits (size-capped) |

Unbound references: render as empty string **or** hard-error if `strict_templates: true` (recommend strict for graph edges; lenient for prompts — TBD).

### 4.2 Structured outputs

Provider steps may declare:

```yaml
output:
  kind: structured
  schema: plan   # built-in
  # or inline JSON Schema
```

Built-in kinds (seed set):

| kind | Purpose | Key fields |
|---|---|---|
| `plan` | Planning / issue list | `done`, `summary`, `issues`, `plan` |
| `verdict` | Pass/fail gate | `pass`, `summary`, `gaps` |
| `text` | Free-form (default) | raw string only |

Structured steps use provider structured-output support plus repair turns (`repair_attempts`).

### 4.3 Working tree

All steps share the q session **working directory**. File side effects are an implicit channel between steps; the graph should still pass explicit plan/verdict data via outputs.

### 4.4 Provider sessions

Each provider continues to use the q session’s native conversation id (Codex thread, Grok session, …). Graph steps that share a provider therefore share chat history **unless** we later add `session: fresh | shared` per step.

Default for first ship: **shared** (current session mapping behavior).

---

## 5. Runtime algorithm (single cursor)

```text
load definition + create/resume run
cursor = entry (or restored cursor)
loop:
  if global guards exceeded → fail
  visit = execute(cursor)
  persist checkpoint
  if visit failed:
    take first edge with on: error (if any) else fail run
  else:
    matches = edges where from == cursor and when(visit, state)
    if empty:
      if step.terminal or cursor is end sink → success
      else → fail (no edge)
    cursor = first match.to
    if cursor is end sink → success/fail per sink kind
```

### 5.1 Checkpoint fields (conceptual)

Extend today’s `WorkflowRun` idea:

- definition snapshot
- `cursor` (step id)
- `visits[]` (append-only)
- `outputs` map step_id → last output
- status, last_error, timestamps
- counters: visits_total, visits_per_step

Resume: store cursor + outputs + visits; re-enter the current step only if its last visit is incomplete; otherwise evaluate edges and continue.

### 5.2 Safety bounds (required)

| Guard | Default (draft) | Role |
|---|---|---|
| `max_visits` | 32 | Hard stop on runaway cycles |
| `max_visits_per_step` | 8 | Hot-node loop detector |
| `max_duration` | optional | Wall clock |
| edge declaration order | — | Deterministic routing |

---

## 6. Breaking change policy

- **No** dual format support.
- **No** `q workflow migrate`.
- Loaders that see `loop.review` / `loop.improve` (or missing `steps`/`edges`) fail with an explicit unsupported-format error and a pointer to the docs.
- Old checkpoint files without graph fields cannot be resumed.
- CLI verbs stay (`run`, `resume`, `status`, `ls`, `init`, …); help text and init template target the graph format only.

Implementation sequencing: [workflow-rewrite-plan.md](./workflow-rewrite-plan.md).

---

## 7. Example: plan → work → verify

Providers: plan **codex**, work **grok**, verify **codex**.

```yaml
version: 1
name: plan-work-verify

entry: plan

limits:
  max_visits: 24
  max_visits_per_step:
    plan: 5
    work: 5
    verify: 5

steps:
  plan:
    type: provider
    provider: codex
    prompt: |
      PLAN for: {{ input }}
      Visit {{ visit }}. Prior work: {{ steps.work }}
      Prior verify: {{ steps.verify }}
      Structured plan only.
    output:
      kind: structured
      schema: plan

  work:
    type: provider
    provider: grok
    prompt: |
      WORK for: {{ input }}
      Plan: {{ steps.plan }}
      Implement plan actions. Summarize changes.

  verify:
    type: provider
    provider: codex
    prompt: |
      VERIFY for: {{ input }}
      Plan: {{ steps.plan }}
      Work summary: {{ steps.work }}
      Inspect tree and tests. Return structured verdict.
    output:
      kind: structured
      schema: verdict

edges:
  - { from: plan,   to: end,    when: "steps.plan.done == true" }
  - { from: plan,   to: work,   when: "steps.plan.done == false" }
  - { from: work,   to: verify, when: always }
  - { from: verify, to: end,    when: "steps.verify.pass == true" }
  - { from: verify, to: plan,   when: "steps.verify.pass == false" }

sinks:
  end:
    type: success
```

Shell-gated variant: `verify` as `type: command` with `command: go test ./...`, edges on command success/error.

Full field reference: [workflow-dag-schema.md](./workflow-dag-schema.md).

---

## 8. Implementation sketch

See [workflow-rewrite-plan.md](./workflow-rewrite-plan.md) for phases and file map. Layering:

1. Schema parse + validate (reject legacy)
2. Condition evaluator
3. Step executors (reuse provider/shell helpers)
4. Graph runner + checkpoints
5. Examples, init template, README; purge old loop code

---

## 9. Open questions

Resolved defaults are in the rewrite plan §7. Remaining product choices:

1. **Artifact store** — large command logs inline vs side files?
2. **CLI naming** — keep `workflow` (recommended) vs rename?
3. **Unreachable-graph detection** — error at load time vs warning?

Record decisions here when frozen.

---

## 10. Summary

| | Legacy (delete) | New engine |
|---|---|---|
| Steps | Fixed review / improve / verify | Free `steps` map |
| Control flow | Hardcoded loop | Conditional `edges` + sinks |
| Loops | Whole-loop `max_iterations` | Graph cycles + visit guards |
| Verify | Command only | Any step type |
| Plan | Field inside review schema | Optional step with `plan` (or custom) |
| Authoring | Fill three slots | Design steps, then wire them |
| Compatibility | n/a | None — rewrite only |

**Steps** are the unit of authoring; **edges** are the unit of orchestration. That is the product: a loopable, conditionally wired workflow graph.
