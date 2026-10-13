# websearchbench

Opt-in live experiment using OEV `qwen3.8-27b`, native System One `gemma4-e4b`, Q's browser runtime, and static HTTP extraction. No Q configuration is written. Ambient OpenAI keys are suppressed.

See [experiment report](../../docs/web-search-systemone-experiment.md) for measured outcomes and limitations. Ordinary tests use local fixtures and never call OEV.

```powershell
go test ./scripts/websearchbench
go run ./scripts/websearchbench -compact -steps 8 -out docs/experiments/rerun.json
go run ./scripts/websearchbench -compact -no-plan -source-check -task python -arms systemone-gated -steps 12 -out docs/experiments/rerun-python.json
go run ./scripts/websearchbench -evaluate docs/experiments/websearch-compact-v2.json,docs/experiments/websearch-python-source-checked.json
```

Arms: `qwen-loop` (generated action each step), `systemone-loop` (native choice each step), `no-generation` (native choice with the task verbatim as query), `deterministic` (first result and four lexical passages), `hybrid` (native actions plus Qwen coverage check and one query repair), `systemone-gated` (native actions and separate native per-clause checks). `-no-plan` suppresses shared Qwen query planning for every arm. `-source-check` adds a native publisher classifier before opening a candidate. `-http-search` skips Chrome in compact mode; live HTTP search may fail independently of model quality.

`-replay` loads earlier successful captures and fetches only missing URLs. It reuses input pages, **not model responses**. Cache differences and server state make wall times unsuitable for universal speedup claims. `-gates TRACE` probes the native per-clause checker on evidence already collected in a trace. `-evaluate` is offline and checks only evidence from official source domains, never the prompt, query, or model reasoning.

The clause lists are manually supplied restatements of the three task questions, not an automatic arbitrary-goal decomposition. Evaluation markers are post-run only. Traces preserve full inputs, responses, evidence, failures, and measured durations; no authorization headers are stored.
