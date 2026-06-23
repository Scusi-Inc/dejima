# The coverage gate — how the test suite stays fresh

The coverage gate is the mechanism that stops a new feature from landing untested.
It lives in `cmd/dejima/coverage_gate_test.go` (the `TestCoverageGate` test) and
runs on every push and PR as part of `go test ./...` — no extra CI job, no Docker.

## What it does

Dejima has two machine-enumerable surfaces of truth:

1. the **CLI** — every runnable cobra command in `newRootCmd()`, and
2. the **API** — every operation in `openapi.yaml` (88 ops; kept in sync with the
   Go routes by the separate route-parity check, `sdk/openapi_parity.py`).

The gate enumerates both, then asserts each one is **referenced by at least one
test**. The "test corpus" is every `*_test.go` file plus the live shell suites
under `scripts/` (`integration.sh`, the `tier3/`/`tier4/` scripts) — nested
worktrees and per-agent checkouts are excluded so a sibling branch can't spoof
coverage.

"Referenced" means:

- **CLI command** — the corpus contains the command's token sequence either as a
  Go quoted-arg run (`"agent", "ls"`) or as a shell invocation (`dejima agent ls`).
- **API operation** — the corpus contains the operation's `operationId` literal,
  or a literal matching its path (each `{param}` standing in for one path segment,
  e.g. `/v1/islands/{name}/hibernate` matches `"/v1/islands/proj/hibernate"`).

If a brand-new command or route lands with no test, it matches nothing and the
gate fails CI. **That is the freshness guarantee: you cannot merge new surface
without a test.**

## The ratchet (waivers)

Not all surface was covered the day the gate landed, so the gate is a one-way
ratchet rather than an all-or-nothing wall. Known-uncovered surface is listed in
`cmd/dejima/testdata/coverage_waivers.txt`, one entry per line:

```
cli agent config
api POST /v1/islands/{name}/hibernate
```

The gate treats a waived entry as a tracked gap (green), but it enforces three
rules that make the list only ever shrink:

- **NEW UNTESTED SURFACE** — a command/op that is neither tested nor waived fails
  CI. Add the test (preferred) or, only if deliberate and reviewed, add a waiver.
- **STALE WAIVER** — a waived entry that now *has* a test fails CI. When you add
  the test, you must delete its waiver line; the ratchet tightens.
- **ORPHAN WAIVER** — a waiver line matching no current command/op (a typo, or
  surface that was removed) fails CI; delete it.

## Adding a feature: the workflow

1. Add the route to `internal/api/*.go` and/or the command to `cmd/dejima/`.
2. Update `openapi.yaml` (the route-parity check enforces this).
3. Write a test that exercises it (a CLI table test, an httptest API test, or a
   line in a live suite). The gate now sees the reference.
4. If the surface was previously waived, delete its line from the waiver file.

Run `go test ./cmd/dejima/ -run TestCoverageGate` locally to check before pushing.
The failure message lists exactly what is uncovered, stale, or orphaned.

## Relationship to the coverage matrix

`docs/testing/test-coverage-matrix.md` is the human-readable rollup. The gate is
the machine-checked floor underneath it: the matrix says *how well* something is
tested (unit vs Docker vs live); the gate guarantees *that something exists at
all* for every command and route.
