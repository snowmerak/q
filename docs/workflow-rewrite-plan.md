# Workflow Engine Rewrite Plan

Status: **plan** — replace the fixed review/improve/verify loop with a free-step conditional graph.  
**Decision: delete the old fixed-loop model; no dual-runner, no migrate, no `loop.review` YAML.**  
The new graph document uses **`version: 1`** (greenfield numbering for the new engine).

Related:

- [workflow-dag-design.md](./workflow-dag-design.md) — product design
- [workflow-dag-schema.md](./workflow-dag-schema.md) — YAML schema

---

## 1. Decision summary

| Topic | Choice |
|---|---|
| Compatibility | **None.** Old loop YAML is rejected. |
| Format version | **`version: 1`** of the **new** engine (greenfield numbering) |
| Old runs on disk | Not resumable; ignore or fail clearly if old shape is loaded |
| CLI surface | Keep `q workflow …` verbs; change semantics and help text |
| Storage dirs | Keep `.q/workflows`, global workflows dir, `workflow-runs` paths |
| Provider / shell | Reuse existing session provider helpers and `executeCommand` |

Rationale: the old loop is a product preset, not a subset of the graph model that is worth carrying. A clean break avoids dual code paths, dual tests, and forever-ambiguous “v1 vs v2” docs.

---

## 2. Current code inventory (remove or replace)

| Area | Files (approx.) | Action |
|---|---|---|
| Fixed loop types + runner | `workflow.go` | **Rewrite** — new types, validate, execute graph |
| CLI | `workflow_cli.go` | **Keep verbs**; point at new load/run; update usage strings |
| Store / resolve / init / get | `workflow_store.go` | **Keep paths**; change `InitWorkflowDefinition` template to graph YAML; validate with new schema |
| Tests | `workflow_test.go`, `workflow_store_test.go` | **Rewrite** fixtures and cases |
| Examples | `examples/workflows/*.yaml` | **Rewrite** all to graph format |
| Docs / README | `README.md`, this `docs/*` | **Rewrite** workflow sections |
| Structured review helpers | inside `workflow.go` today | **Generalize** into step output kinds (`plan`, `verdict`, …) |

Do **not** keep:

- `WorkflowLoop` with hard-coded `Review` / `Improve` / `Verify`
- `max_iterations` whole-loop counter as the only control mechanism
- Template vars limited to `input` / `iteration` / `review` / `improve` / `verify`
- Any “compile old YAML → graph” adapter

**Reuse as libraries (no user-facing old API):**

- `callWorkflowProvider` / session provider calls
- `extractStructuredPayload` + repair-turn pattern
- `executeCommand`
- run directory + atomic save patterns
- workflow path resolution (project vs global)

---

## 3. Target shape (what ships)

### 3.1 Definition (YAML only format)

```yaml
version: 1
name: plan-work-verify
entry: plan
limits: { max_visits: 24, ... }
steps: { ... }
edges: [ ... ]
sinks: { end: { type: success } }
```

See schema doc for fields. After rewrite, **any file with the old `loop.review` shape fails validation** with an explicit error, e.g.:

```text
unsupported workflow format: expected graph document (version 1 with steps/edges); legacy loop.review is not supported
```

### 3.2 Runtime

Single-cursor graph runner:

1. Load + validate definition  
2. Create run checkpoint (definition snapshot, `cursor = entry`, empty outputs)  
3. Loop: execute step → store visit/output → evaluate edges → move cursor or terminate  
4. Guards: `max_visits`, `max_visits_per_step`, optional duration  
5. Resume from checkpoint mid-graph  

### 3.3 Module split (suggested Go layout)

Prefer clarity over many packages for a small binary; can stay in `package main` with file splits:

| File | Responsibility |
|---|---|
| `workflow_types.go` | Definition + run + visit structs, YAML tags |
| `workflow_validate.go` | Schema validation, id rules, edge checks |
| `workflow_template.go` | `{{ input }}`, `{{ steps.* }}` rendering |
| `workflow_condition.go` | `when` keywords + small expression eval |
| `workflow_step.go` | Execute provider / command steps; structured repair |
| `workflow_run.go` | Graph cursor loop, limits, fail/success |
| `workflow_store.go` | Paths, list, init template, download validate |
| `workflow_cli.go` | CLI |
| `workflow_*_test.go` | Unit tests per concern |

Optional later: `internal/workflow` package if the main package grows too large.

---

## 4. Implementation phases

### Phase 0 — Spec freeze (docs only)

**Done when:**

- [x] Design doc states clean break (no legacy load)
- [x] Schema doc is the only authoring format (`version: 1` graph)
- [x] This rewrite plan accepted
- [ ] Open questions in design doc resolved enough to code (see §7)

**Exit:** schema field names frozen for the first coding milestone.

---

### Phase 1 — Types, load, validate

**Deliverables:**

- New definition structs matching schema
- `LoadWorkflow(path)` parses YAML, rejects non-graph / wrong version
- Validation: unique step ids, entry exists, edges reference steps/sinks, provider enums, verify step shapes, limits > 0
- Unit tests: valid graph, missing entry, bad edge, empty steps, legacy YAML rejected

**Not yet:** execution.

---

### Phase 2 — Template + condition engines

**Deliverables:**

- Template render with nested path lookup (`steps.plan.done`)
- Condition eval: `always` / `never` / expressions (`==`, `!=`, `&&`, `||`, comparisons)
- Missing path policy: **error on edge evaluation** (fail closed)
- Unit tests: render cases, condition table tests

---

### Phase 3 — Step execution

**Deliverables:**

- `provider` step: call existing provider helpers; optional structured output + repair
- Built-in schemas: `plan`, `verdict` (rename away from `plan_v1` / `verdict_v1` unless preferred)
- `command` step: shell via `executeCommand`; capture exit + combined output
- Visit record written to memory (not full graph loop yet)
- Unit tests: mock or table-level validation of structured outputs; command success/failure classification

---

### Phase 4 — Graph runner + checkpoints

**Deliverables:**

- `ExecuteWorkflow` cursor loop
- Edge selection (`on: success|error`, first matching `when`)
- Sinks `success` / `failure`
- Limits enforcement
- Persist run after each visit (atomic write, same dir as today)
- `resume` continues from `cursor` + `outputs`
- Integration-style tests with fake step executor if needed to avoid live LLM

**CLI:** `run` / `resume` / `status` / `runs` work on new run JSON shape.

---

### Phase 5 — Store, init, examples, docs

**Deliverables:**

- `workflow init` writes a small graph template (e.g. plan→work→verify loop)
- `workflow get` validates downloaded file with **new** validator only
- Replace all `examples/workflows/*` with graph YAML
- Delete or rewrite smoke examples (`structured-smoke`, `full-loop-smoke`) as graph equivalents
- README workflow section rewritten
- Design/schema status → “implemented” for the shipped subset

---

### Phase 6 — Cleanup and hard cut

**Deliverables:**

- Remove dead types/functions from old loop (`ReviewOutput` only if unused; old template var map; old phase names)
- Grep for `loop.review`, `max_iterations`, `ImproveDone` — zero hits in non-doc code
- Old `workflow-runs` JSON: `resume` returns clear error if `definition` lacks `steps`/`edges`
- Tag/release note: **breaking change** for workflow authors

---

## 5. File-level change map

| Path | Change |
|---|---|
| `workflow.go` | Split/replace with modules in §3.3 |
| `workflow_cli.go` | Usage strings; status printing for cursor/visits |
| `workflow_store.go` | Init template; download validation |
| `workflow_test.go` | Full rewrite |
| `workflow_store_test.go` | Fixtures → graph YAML |
| `examples/workflows/improve-until-clean.yaml` | Graph rewrite (same intent) |
| `examples/workflows/improve-until-clean-claude.yaml` | Graph rewrite |
| `examples/workflows/full-loop-smoke.yaml` | Graph smoke |
| `examples/workflows/structured-smoke.yaml` | Graph smoke or drop |
| `examples/workflows/plan-work-verify.yaml` | Canonical graph example |
| `README.md` | Breaking workflow section |
| `docs/workflow-dag-*.md` | Align versioning; mark implemented areas |

No migration tool. Authors re-write YAML by hand (examples show patterns).

---

## 6. Example rewrite: old → new

**Old (deleted format):**

```yaml
version: 1
name: improve-until-clean
loop:
  max_iterations: 3
  review: { provider: grok, prompt: "..." }
  improve: { provider: codex, prompt: "..." }
  verify: { command: go test ./... }
```

**New (only format):**

```yaml
version: 1
name: improve-until-clean
entry: review
limits:
  max_visits: 12
  max_visits_per_step:
    review: 3
    improve: 3
    verify: 3
steps:
  review:
    type: provider
    provider: grok
    prompt: |
      Review {{ input }}. Visit {{ visit }}.
      Last improve: {{ steps.improve }}
      Last verify: {{ steps.verify }}
    output: { kind: structured, schema: plan }
  improve:
    type: provider
    provider: codex
    prompt: |
      Apply plan for {{ input }}:
      {{ steps.review }}
  verify:
    type: command
    command: go test ./...
edges:
  - { from: review, to: end, when: "steps.review.done == true" }
  - { from: review, to: improve, when: "steps.review.done == false" }
  - { from: improve, to: verify, when: always }
  - { from: verify, to: review, when: always, on: success }
  - { from: verify, to: improve, when: always, on: error }
sinks:
  end: { type: success }
```

Canonical multi-provider plan/work/verify (codex / grok / codex) lives in `plan-work-verify.yaml` per design doc.

---

## 7. Decisions to lock before Phase 1 coding

| # | Topic | Proposal for freeze |
|---|---|---|
| D1 | Format `version` field | `1` only (new engine) |
| D2 | Legacy YAML | Hard error with message pointing at docs |
| D3 | Condition language | Keywords + small expr subset from schema doc |
| D4 | Missing `{{ steps.x }}` in prompts | Empty string |
| D5 | Missing path in **edge** `when` | Error (fail closed) |
| D6 | Built-in schemas | Names: `plan`, `verdict` |
| D7 | Parallel steps | Not in first ship |
| D8 | Provider session | Always shared (current session mapping) |
| D9 | Old run resume | Fail: `legacy workflow run is not supported` |
| D10 | Init template | `plan-work-verify`-style mini graph |

Update design/schema docs when these are accepted.

---

## 8. Test plan

| Layer | Cases |
|---|---|
| Validate | happy path; duplicate ids; bad provider; edge to missing step; no matching default; legacy loop rejected |
| Template | flat + nested paths; unknown path → empty in prompts |
| Condition | `always`, comparisons, `&&`/`||`, missing path error |
| Step | structured plan accept/reject; command exit 0/1 |
| Runner | linear A→B→end; branch on `done`; cycle with limit trip; error edge; resume after mid-run save |
| Store | init creates valid graph; resolve project over global; download rejects invalid graph |
| CLI smoke | `workflow ls`, `run` against fixture with mocked executor if available |

Live provider smoke (optional, manual): `plan-work-verify` against a throwaway objective.

---

## 9. Risks

| Risk | Mitigation |
|---|---|
| Scope creep (parallelism, rich language) | Phases 1–4 ship sequential + small expr only |
| Checkpoint schema churn | Freeze run JSON in Phase 4 before polish |
| Prompt breakage for existing users | README breaking note + rewritten examples; no silent accept of old YAML |
| Large `workflow.go` rewrite in one PR | Prefer phased PRs: validate → template/cond → steps → runner → examples |
| Expression parser bugs | Table-driven tests; start with keywords + `path == bool` only if needed, then expand |

---

## 10. PR / milestone breakdown (suggested)

| Milestone | Scope | Rough order |
|---|---|---|
| M1 | Types + load + validate + reject legacy | Phase 1 |
| M2 | Templates + conditions | Phase 2 |
| M3 | Step executors + schemas | Phase 3 |
| M4 | Graph runner + checkpoint + resume | Phase 4 |
| M5 | Store init, examples, README, dead code purge | Phases 5–6 |

Each milestone should leave `go test ./...` green. M1–M3 may keep CLI non-functional for `run` until M4, **or** stub `run` with “not implemented” — prefer M1–M4 as a short stack landed closely so main is never half-broken for long.

**Aggressive alternative:** one branch rewriting all of `workflow*.go` + examples + README, reviewed as a single breaking PR. Acceptable if the tree stays small; still follow phase order inside the branch.

---

## 11. Out of scope for this rewrite

- Agent Skills changes  
- New providers  
- Workflow marketplace  
- GUI  
- Automatic conversion of old YAML files  
- Keeping old loop for “simple mode”  

---

## 12. Success criteria

1. No code path executes `loop.review` / `loop.improve` / `loop.verify`.  
2. Authors define arbitrary steps and connect them with conditional edges.  
3. Cycles work with visit limits.  
4. Provider and command steps both work as first-class nodes.  
5. `q workflow run|resume|status|ls|init|…` operate on the graph format only.  
6. Examples and README describe only the graph model.  
7. Docs (design + schema + this plan) agree on one version and zero legacy support.

---

## 13. Next action

1. Confirm §7 freezes (or edit them).  
2. Align [workflow-dag-design.md](./workflow-dag-design.md) and [workflow-dag-schema.md](./workflow-dag-schema.md) to **clean break + `version: 1`**.  
3. Start **Phase 1** implementation when ready (separate coding task).
