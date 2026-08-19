# A guard whose failure mode is silence needs a control that proves it can still see

The *why* is already written up: see **[Standing rule: prove the check can see a
failure](test-coverage-matrix.md#standing-rule-prove-the-check-can-see-a-failure)**,
which carries the evidence — eight instances in one week, five tools, one
failure — and the corollaries about weak reasons and distributed instruments.

This note is the practitioner's half: **what a control looks like in code**, one
shape at a time, and when you don't need one. It exists because "prove the check
can see a failure" is easy to agree with and easy to skip, and three places in
the tree now do it concretely enough to copy.

## The rule

> When a test's only failure mode is *not noticing*, add a second test whose only
> job is to prove the first one can still see.

Call the first one the **guard** and the second one the **control**. The control
never tests the product. It tests the instrument.

## Two more instances, both from fixing the first eight

Added to the tally in the matrix:

- **A mutation that didn't compile.** Unwrapping a call site produced
  `NewServer((`, and the package failed to build. `[build failed]` is not the
  guard firing — it's the guard never running — and the red read as a pass.
- **An in-flight counter that raced its subject.** Sampling with a zero-length
  timeout let the timer beat the drained `WaitGroup`, so a store with nothing
  running reported "still busy". It inflated the published number seventeen-fold.

## The three shapes, and the control each one needs

### 1. The guard that can't see — needs a positive control

The guard looks for a condition that, in this environment, can never arise. It
passes for a reason unrelated to what it asserts.

**Real case.** CLI tests must not touch the machine's keychain, asserted as
`Backend() == "file"`. But this container has no `secret-tool`, so the file
backend is selected *regardless* — the assertion passed with the guard deleted.

**Control.** Manufacture the condition the guard defends against. A stub
executable of the platform tool's name at the front of `PATH` makes the keychain
genuinely selectable, so `"file"` can only be the guard's doing. Then a third
test asserts the stub still works — if the stub silently breaks, the other two go
hollow and stay green.

→ `cmd/dejima/cli_secrets_isolation_test.go`

### 2. The guard that isn't looking at anything — needs a non-emptiness assert

Source-scanning and enumeration guards fail open. Rename what they match or move
the files they read, and they report "all clear" over an empty set.

**Control.** Assert the guard found *something* before it reports that nothing
was wrong:

```go
if seen == 0 {
    t.Fatal("found no api.NewServer calls at all — this guard is no longer watching anything")
}
```

One line. It separates "I checked and it's fine" from "I checked nothing" —
different sentences that should not print the same.

→ `cmd/dejima/background_join_test.go`

### 3. The guard nothing can violate — needs a lethal mutation

If no realistic change makes the guard fail, it isn't a guard. Mutation testing
is the control, with two traps of its own:

- **Assert the mutation applied.** `assert s != before, "MUTATION DID NOT APPLY"`
  in the script. A regex that quietly doesn't match, or a `git diff` blind to an
  untracked file, both produce a clean "survived".
- **Compile the mutant before reading the result.** Run `go vet` on the mutated
  tree and abort if it fails. Otherwise a broken build gives you a red that looks
  like the guard working.

→ `internal/api/background_join_wiring_test.go`,
  `internal/api/primary_launch_parity_test.go`

## Instruments get the same treatment

A measurement you are about to publish is a guard pointed at reality, and it
fails the same ways.

**Validate against a known positive and a known negative before trusting the
number.** An in-flight counter should read 1 against a deliberately blocked
worker and 0 against a drained one. Two seconds of work; it caught the
seventeen-fold error above before it reached a commit message.

Two corollaries:

- **An instrument that fails loudly and flatteringly is worse than one that
  fails silently.** A silent failure leaves you puzzled. An inflated number that
  makes your own finding look more impressive gets published — nothing about the
  output invites a second look.
- **When a value is supposed to be static, read it twice with time in between.**
  A positive control validates an instrument *at sample time*, which is exactly
  when a mutating instrument behaves correctly; only resampling reaches past
  that. Treat disagreement as information about the instrument first and the
  subject second.

## When not to do this

This is not "double every test". A control earns its place only when the guard's
failure mode is **silence**. Assertions that fail loudly — a wrong value, a
missing field, a returned error — already tell you.

The question to ask: *if this check quietly stopped working, would anything look
different?* If the honest answer is no, write the control.

## Where the pattern already lives

| Guard | Control | What the control proves |
| --- | --- | --- |
| `internal/api/primary_launch_parity_test.go` | prologue derived **from** `agentLaunchScript` rather than hardcoded | the check follows the daemon side instead of matching a stale literal |
| `cmd/dejima/cli_secrets_isolation_test.go` | `TestKeychainStubMakesTheKeychainBackendReachable` | the keychain is genuinely selectable, so `"file"` means the guard worked |
| `internal/api/background_join_wiring_test.go` | `TestJoinWiringGuardRecognisesAnUnwrappedCall` | the matcher still fires on an unwrapped call |
| `cmd/dejima/background_join_test.go` | `seen == 0` fatal | the guard is still reading the package it guards |

## The one-line version

**Prove the check can see a failure before trusting its silence.**
