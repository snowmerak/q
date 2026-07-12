# Workflow Graph Schema (Draft)

Status: **draft** — schema only; not implemented.  
Design: [workflow-dag-design.md](./workflow-dag-design.md) · Rewrite plan: [workflow-rewrite-plan.md](./workflow-rewrite-plan.md)

**Only supported format** after the rewrite. Legacy `loop.review` / `loop.improve` / `loop.verify` documents are **rejected**.

- Document `version`: **`1`** (first version of the graph engine; greenfield numbering)
- Encoding: YAML (JSON-equivalent)
- Download size ceiling: keep current 1 MiB unless revisited

---

## 1. Top-level document

```yaml
version: 1                    # required; integer 1 (graph engine only)
name: string                  # required; non-empty
description: string           # optional

entry: step_id                # required; must exist in steps

limits:                       # optional; defaults applied if omitted
  max_visits: int
  max_visits_per_step:        # optional map
    step_id: int
  max_duration: string        # optional duration, e.g. "30m"

steps:                        # required; map[step_id]Step
  step_id: { ... }

edges:                        # required; list[Edge]
  - { ... }

sinks:                        # optional; map[sink_id]Sink
  end: { type: success }

defaults:                     # optional
  repair_attempts: int        # structured provider repair turns
  strict_templates: bool      # reserved; edge eval is fail-closed on missing paths
```

### 1.1 Validation rules (document)

| Rule | Error if violated |
|---|---|
| `version == 1` | unsupported version |
| document has `steps` and `edges` (not `loop`) | unsupported / legacy format |
| `name` non-empty | name required |
| `entry` ∈ `steps` | unknown entry |
| every `steps` key is a valid `step_id` | invalid id |
| every edge `from` ∈ `steps` | unknown from |
| every edge `to` ∈ `steps` ∪ `sinks` ∪ built-in sinks | unknown to |
| `limits.max_visits > 0` when set | invalid limit |

### 1.2 Legacy rejection

If the document contains any of:

- top-level `loop` with `review` / `improve`
- absence of `steps` map

→ fail load with a message that the fixed-loop format was removed and authors must use the graph schema (link to docs).

### 1.3 `step_id` / `sink_id` grammar

```text
step_id  := [a-z][a-z0-9_-]{0,63}
sink_id  := same as step_id
```

Reserved ids (cannot be step names): `end`, `success`, `fail`, `input`, `visit`, `step`, `steps`, `history`.

---

## 2. Step

### 2.1 Common fields

```yaml
steps:
  plan:
    type: provider | command   # required
    description: string        # optional
    terminal: bool             # optional; default false
```

If `terminal: true` and no outgoing edge matches, the run **succeeds**.

### 2.2 Type: `provider`

```yaml
steps:
  plan:
    type: provider
    provider: codex | grok | claude | agy   # required
    prompt: |                               # required
      ...
    output:                                 # optional; default { kind: text }
      kind: text | structured
      schema: plan | verdict                # built-in name
      json_schema: object                   # optional inline JSON Schema
    repair_attempts: int                    # optional
    session: shared | fresh                 # optional; first ship may only implement shared
```

Rules:

- `provider` required and allowed.
- `prompt` required, non-empty after trim.
- If `output.kind == structured`, set exactly one of `schema` (built-in) or `json_schema`.
- Structured validation failure → repair turns up to `repair_attempts` (default from `defaults` or `2`).

### 2.3 Type: `command`

```yaml
steps:
  test:
    type: command
    command: string            # required; templates allowed
```

- Exit `0` → visit success; non-zero → visit failure.
- Payload:

```json
{
  "stdout_stderr": "...",
  "exit_code": 1
}
```

`{{ steps.test }}` → combined stdout/stderr string.  
`{{ steps.test.exit_code }}` → numeric field access.

### 2.4 Built-in structured schemas

#### `plan`

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["done", "summary", "issues", "plan"],
  "properties": {
    "done": { "type": "boolean" },
    "summary": { "type": "string", "minLength": 1 },
    "issues": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "severity", "description", "evidence", "acceptance_criteria"],
        "properties": {
          "id": { "type": "string", "minLength": 1 },
          "severity": { "type": "string", "enum": ["low", "medium", "high"] },
          "description": { "type": "string", "minLength": 1 },
          "evidence": { "type": "string", "minLength": 1 },
          "acceptance_criteria": { "type": "string", "minLength": 1 }
        }
      }
    },
    "plan": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["issue_id", "action"],
        "properties": {
          "issue_id": { "type": "string", "minLength": 1 },
          "action": { "type": "string", "minLength": 1 }
        }
      }
    }
  }
}
```

Semantic checks:

- `done == true` ⇒ `issues` and `plan` empty
- `done == false` ⇒ ≥1 issue; every issue id has a plan item

#### `verdict`

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["pass", "summary"],
  "properties": {
    "pass": { "type": "boolean" },
    "summary": { "type": "string", "minLength": 1 },
    "gaps": {
      "type": "array",
      "items": { "type": "string" }
    },
    "evidence": { "type": "string" }
  }
}
```

Semantic checks:

- `pass == true` ⇒ `gaps` empty or omitted
- `pass == false` ⇒ non-empty `gaps` (hard error if missing/empty)

---

## 3. Edge

```yaml
edges:
  - from: step_id
    to: step_id | sink_id
    when: Condition
    on: success | error | always   # default success
    description: string            # optional
```

| `on` | When considered |
|---|---|
| `success` (default) | visit completed without executor error |
| `error` | provider error, non-zero exit, schema failure after repairs |
| `always` | after visit finishes (prefer explicit success/error) |

Matching: filter by `from` + `on` → evaluate `when` in **declaration order** → first true wins → else fail closed (unless step `terminal`).

---

## 4. Condition language

### 4.1 Keywords

| Keyword | Meaning |
|---|---|
| `always` | true |
| `never` | false |
| `success` | current visit succeeded |
| `failure` | current visit failed |

### 4.2 Expressions (first ship)

```text
expr     := or_expr
or_expr  := and_expr ( "||" and_expr )*
and_expr := cmp ( "&&" cmp )*
cmp      := primary ( ("==" | "!=" | ">" | ">=" | "<" | "<=") primary )?
primary  := "true" | "false" | NUMBER | STRING | path | "(" expr ")" | "!" primary
path     := "steps" "." step_id ( "." field )*
         | "input" | "visit" | "step"
```

Examples:

```text
always
steps.plan.done == true
steps.verify.pass == false
steps.test.exit_code == 0
steps.verify.pass == false && visit < 10
```

Missing path during **edge** evaluation → **error** (fail closed).  
Missing path during **prompt** template render → empty string.

---

## 5. Sinks

```yaml
sinks:
  end:
    type: success | failure
    message: string
```

If `sinks` omitted: `to: end` ⇒ success; `to: fail` ⇒ failure.

---

## 6. Limits

```yaml
limits:
  max_visits: 32
  max_visits_per_step:
    plan: 5
  max_duration: 1h
```

| Field | Default if omitted |
|---|---|
| `max_visits` | `32` |
| `max_visits_per_step` | none (global only) |
| `max_duration` | none |

---

## 7. Templates

Syntax: `{{ name }}` / `{{ name }}` with optional spaces; dotted paths for fields.

Context:

```json
{
  "input": "string",
  "visit": 3,
  "step": "work",
  "steps": {
    "plan": { "raw": "...", "done": false, "summary": "..." },
    "work": { "raw": "..." }
  }
}
```

No legacy aliases (`{{ review }}`, `{{ improve }}`, `{{ iteration }}`). Use `{{ steps.* }}` and `{{ visit }}`.

---

## 8. Run checkpoint (draft)

```json
{
  "id": "uuid",
  "workflow_name": "plan-work-verify",
  "workflow_path": "/abs/path.yaml",
  "definition": { },
  "session_name": "default",
  "working_dir": "/project",
  "input": "objective",
  "status": "pending|running|completed|failed",
  "cursor": "work",
  "visit_count": 4,
  "visits_per_step": { "plan": 2, "work": 1 },
  "outputs": {
    "plan": { "raw": "...", "parsed": { "done": false } }
  },
  "visits": [],
  "last_error": "",
  "created_at": "...",
  "updated_at": "..."
}
```

Resume of checkpoints missing `cursor` / graph `definition.steps` → hard error (legacy run).

---

## 9. Complete examples

### 9.1 plan → work → verify with loop

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
      PLAN: {{ input }}
      Last verify: {{ steps.verify }}
    output: { kind: structured, schema: plan }
  work:
    type: provider
    provider: grok
    prompt: |
      WORK: {{ input }}
      Plan: {{ steps.plan }}
  verify:
    type: provider
    provider: codex
    prompt: |
      VERIFY: {{ input }}
      Plan: {{ steps.plan }}
      Work: {{ steps.work }}
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

### 9.2 Command verify + error edge

```yaml
version: 1
name: work-then-test
entry: work
steps:
  work:
    type: provider
    provider: grok
    prompt: "Implement {{ input }}"
  test:
    type: command
    command: go test ./...
edges:
  - { from: work, to: test, when: always }
  - { from: test, to: end, when: always, on: success }
  - { from: test, to: work, when: always, on: error }
sinks:
  end: { type: success }
limits:
  max_visits_per_step:
    work: 5
    test: 5
```

---

## 10. Acceptance criteria (schema ready to implement)

- [x] Single format: graph `version: 1`
- [x] No legacy load path
- [x] Step types `provider` + `command`
- [x] Built-ins `plan` + `verdict`
- [x] Condition keywords + expression subset
- [x] Edge `on` + first-match order
- [x] Limits behavior
- [x] Checkpoint shape for resume
- [x] Golden example `plan-work-verify`
- [ ] Frozen by implementer before Phase 1 coding (see rewrite plan §7)
