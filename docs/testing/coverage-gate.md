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

### Comments are not coverage

The corpus is stripped of comments before it is searched — Go through
`internal/srcscan`'s scanner, shell through whole-line `#` stripping. **A
reference has to be in code.** A file that cannot be stripped fails the gate; it
is never scanned raw.

This is not tidiness. The gate credited any *mention*, so a comment explaining
why operators should **not** be sent to `dejima auth push` marked that command's
waiver STALE — and the prescribed cure for a stale waiver is to delete it, from a
command that still had no test. **A false positive here tightens the ratchet onto
untested surface**, which is worse than one that merely nags, because acting on
it removes protection. The workaround the author found (don't spell the token
sequence in prose) is not one the next author would know to make.

Turning the strip on revealed ten CLI commands credited by prose alone. All ten
had real tests; what those tests lacked was the command PATH in code — they
walked a detached constructor (`newLocalCmd().Commands()`, comparing bare names)
while the full path sat in the comment above. So the fix was to assert the paths
from the root command via `rootCommandPaths` (`cmd/dejima/cmd_paths_test.go`),
which is the stronger assertion anyway: a family that loses its `AddCommand` call
passes the constructor form and fails this one.

### A mention is not an invocation

For CLI commands the gate accepts each form only where it *is* an invocation:

| file | counts | does not count |
| --- | --- | --- |
| Go test | `"ssh", "enroll"` — a quoted-arg run, what `SetArgs` and `runCLI` are built from | `"dejima ssh enroll"` — the human spelling in a string |
| shell suite | `dejima ssh enroll` — the script really runs it | a `#` comment |

The human spelling in a Go string used to count, which is issue #335: an
expected-output assertion checking that an error names the right remedy —
`strings.Contains(hint, "dejima ssh enroll")` — marked `cli ssh enroll` a stale
waiver. Green was one `sed` away from a permanent claim of coverage that did not
exist, and since naming a remedy in an error is good practice, the gate was
penalising the pattern it should reward. One test in the tree had already
started building the literal by concatenation (`"auth" + " push"`) to dodge it;
that workaround is gone.

Stale-waiver and uncovered reports now name the **file and line** that matched,
so a demand to delete a waiver can be checked in one look instead of grepped
for.

**Writing a reference on purpose is fine and expected** — `root.SetArgs([]string{
"profile", "switch", "cloud"})` in a test that actually runs the verb is exactly
what the gate is looking for. What no longer counts is a sentence about it.

When a command genuinely cannot be invoked in a test — it installs software,
downloads gigabytes, or waits for a browser — waive it and say which of those it
is, naming the tests that cover the logic underneath. Four commands are in that
position today (`local install`, `local pull`, `voice install`, `github
connect`).

`TestCoverageGateIgnoresComments` is the control: prose does not count, code
still does, a `//` inside a string literal survives (the Go scanner decides, not
a regex), and an unparseable file errors.

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
2. Update `openapi.yaml` — the route-parity check enforces the route, and
   `sdk/openapi_field_parity.py` enforces the FIELDS: every `json:` tag on a
   documented request/response type and every query param the handler reads.
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
