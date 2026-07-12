---
name: verify-go
description: >
  Choose and run build/test/race verification for a Go project and interpret
  failures. verify, go test, ci.
tags: [verify, go, test, ci]
---

# Verify Go Project

Run interactive verification similar to a workflow `verify` step (e.g. `go test ./...`).

## Steps

1. Confirm the module root (`go.mod`) and package layout.
2. Default to `go test ./...`.
3. Honor Additional instructions for race, package scope, or benches:
   - Example: `go test -race ./...`
   - Example: `go test ./path/to/pkg -count=1`
4. On failure, capture package, test name, and the key error lines.
5. Suggest likely file locations when clear. Fix only if the user asks.

## Constraints

- Verification is the goal; do not change code by default.
- Long benchmarks or network-heavy tests only when requested.

## Output

- Commands run
- Pass / fail
- On fail: package, test name, core error text, suggested next actions
