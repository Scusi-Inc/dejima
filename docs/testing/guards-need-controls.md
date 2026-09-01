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

## The shapes, and the control each one needs

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

→ `cmd/dejima/background_join_test.go`,
  `internal/api/background_join_wiring_test.go`

The `internal/api` one is worth reading as a cautionary tale rather than an
example: it shipped WITHOUT the `seen == 0` fatal and was caught within the hour
by the person writing the `cmd/dejima` twin. It already asserted `len(files) !=
0`, which looks like the same thing and is not — that catches "I am not reading
the package", while a renamed constructor leaves the files readable and the
matches at zero. A guard can have one non-emptiness assert and still fail open
through the other.

**The special case that keeps recurring: the guard matches the comment.** A
source-scanning guard looks for a token; the comment *explaining* that token
contains it. Delete the real thing, leave the prose, and the guard passes — on
the sentence about the code rather than the code.

This happened four times in one week: three times on `DEJIMA_ROLE`, then once on
a checksum step, the last written by someone who had read the write-up of the
first three *that same afternoon*. That is the useful part. It is not evidence
about any of the four authors; it is evidence about what a write-up can do.
**Documenting a failure mode does not prevent it**, and the instinct it provokes
— write it down harder — is the wrong one.

So the remedy here is mechanical rather than editorial. `internal/srcscan`
strips comments once, exactly, and any guard that scans source uses it instead of
remembering:

```go
src, ok := srcscan.StripGoComments(string(b))   // or StripLineComments(script, "#")
if !ok {
    t.Fatal("could not parse the file — scanning it raw would risk a half-stripped miss")
}
```

Note which way it errs, because the two mistakes are not symmetric. Strip too
little and a guard matches a comment: noisy, and instantly visible to whoever is
staring at the failure. Strip too much and the guard stops seeing real code — it
passes, silently, for the same reason the code is broken, which is this exact bug
rebuilt one layer down. `StripLineComments` therefore removes whole-line comments
only and leaves trailing ones, and the stripper carries the control that a
function returning `""` would fail.

**And when the mechanism is swept across the existing guards, the sweep needs
its own control.** Converting thirty-five guards to a shared stripper is
thirty-five opportunities to turn one of them into a guard that now matches
nothing — and every one of those will still pass, because a guard matching
nothing is exactly what a green run looks like. "They all still pass" is
therefore not evidence that the migration worked; it is the same sentence you
would get if it had failed. Mutation-check a sample as part of the sweep, not all
thirty-five, but enough that the green has a second source. This is the
mechanism's own control argument applied one level up, to the migration rather
than to the tool.

*(The recurrence count and the "documenting it did not prevent it" reading are
d1's; the fourth instance was reported by d3 against their own guard, along with
the point about the sweep.)*

### 3. The guard nothing can violate — needs a lethal mutation

If no realistic change makes the guard fail, it isn't a guard. Mutation testing
is the control — and it has five traps of its own, each found the hard way:

- **Assert the mutation applied.** `assert s != before, "MUTATION DID NOT APPLY"`
  in the script. A regex that quietly doesn't match, or a `git diff` blind to an
  untracked file, both produce a clean "survived".
- **Assert it hit the site you meant.** `assert s.count(old) == 1` before
  replacing. The weaker check above passes happily when your pattern matches in
  two places and you mutate the wrong one — the file changed, the mutation is
  real, and it is somewhere else entirely. Note that `sed` and `perl` without
  `/g` replace the FIRST match *silently*: an ambiguous pattern does not error,
  it quietly mutates the wrong site, which is exactly how a confident zero gets
  produced.
- **Compile the mutant before reading the result.** Run `go vet` on the mutated
  tree and abort if it fails. Otherwise a broken build gives you a red that looks
  like the guard working.
- **Prove the UNMUTATED tree is clean first.** If the baseline already fails,
  every mutation "passes" for reasons that have nothing to do with the mutation,
  and the results are noise wearing the shape of evidence.
- **Prove your mutation MACHINERY is inert.** `sdk/openapi_field_parity.py`'s
  self-test rewrites the spec with a no-op change and re-checks before applying
  the real mutation, so a red can never be the YAML round-trip rather than the
  edit. Any harness that parses-and-reserialises, reformats, or regenerates has
  this exposure; the check for it is one extra run.

**The second trap failed in a new direction, which is why it earns its own
line.** Reviewing a fail-safe path, a string that appeared twice in `server.go`
was mutated in the *purge* guard while the *agent-removal* guard was the one
under test. Zero failures, honestly obtained — and the conclusion forming was
not "my method is broken" but "the author's fail-safe path is untested". Every
other trap in this document fails toward *all clear*; this one fails toward
**accusing someone else's work**, which is at least as easy to act on and much
harder to walk back. Upgrading the assertion from "something changed" to
"exactly one site matched" caught it immediately.

The first two are one line each in the mutation script, and the fourth is one
run. Write them before you need them; you will not think to add them at the
moment you are reading a surprising zero.

**Running the rule backwards, over work you already believe is finished.** The
second trap was written up after the mutations it describes had already shipped.
The honest thing to do with a rule that arrives late is to point it at what you
already believe is done — so, in d2's words:

> Three anchors from my own mutation runs, and how many places each actually
> matches in the file I ran them against: `== c.island` (3), `== c.agent` (4),
> `m.dirtyOps[c.island] = "purging"` (2). Any of those, used bare with a
> first-match replace, mutates whichever site comes first — not the one I meant.
> Two of the three would have hit a different `case` in the same switch.
>
> They did not, because I had used multi-line anchors that included the
> surrounding `case` line, and each of those matched exactly once. So the
> results stand. But I want to be precise about *why* they stand: **they held by
> luck of habit, not by method.** I used a longer anchor because longer anchors
> feel safer, not because I had counted the matches.

That is the sentence worth keeping, because it names something invisible in a
green result: **a correct result produced by an unverified method and a correct
result produced by a verified one look identical** — in the diff, in the test
output, and in the write-up. The only person who can tell them apart is the one
who did it, and only if they ask.

Which is also why the check tends not to get run. Retroactively it is no longer
"validate my method", it is "find out whether my finished work was wrong all
along" — the same command, a much less comfortable question. Run it before, and
it costs nothing.

→ `internal/api/background_join_wiring_test.go`,
  `internal/api/primary_launch_parity_test.go`

### 4. The guard that only *sometimes* sees — needs a deterministic reproduction

The guard is correct. It is simply under-powered: the condition it watches for
occurs on some fraction of runs, so most runs are green and the occasional red
is indistinguishable from noise.

This is the most expensive shape in the file, and the cost is not technical.

**The tell.** *It survives a mutation that should kill it.* Break the thing the
guard exists to protect and run the guard: if it still passes, it is not
watching what you think. That check takes a minute and nobody thinks to run it
on a test already labelled flaky — which is exactly the test that most needs it.

**Real case (#338).** `internal/wsl` translated a bare `EOF` into "socat isn't
installed". The translation raced: the diagnosis is read from the subprocess's
stderr, which `exec` copies on its own goroutine, while the reader's EOF was
released immediately. About one run in two hundred, the caller got the raw EOF
the code existed to replace.

The test could see it — and did, at roughly one run in two hundred. With the fix
removed, and each mutation compile-checked first:

| | 30 runs, fix removed | measured by |
| --- | --- | --- |
| the deterministic test | 30 failures | both of us |
| the original test | 0 failures | first observer |
| the original test | 1 failure | second observer |

Those last two rows are the point, not a discrepancy to tidy away. **The
original guard's signal is weak enough that two people measuring the same broken
code got different answers**, and either could reasonably round theirs to
"flaky". A guard you have to sample repeatedly to hear is one that will be
misread by whoever samples it once.

**The control.** A fixture that forces the ordering rather than hoping for it.
Here, a fake subprocess that closes stdout *before* writing stderr, making the
race certain instead of occasional. Pick an ordering the real system genuinely
produces — `wsl.exe`'s stderr crosses a virtualization boundary, so arriving
after the pipe closes is the actual case, not a contrivance. Then add the
now-familiar control on the control: assert the fixture still produces the
ordering it is named for, or the deterministic test quietly degrades into a
duplicate of the probabilistic one and keeps passing.

→ `internal/wsl/wsl_test.go` — `socat-missing-late`,
  `TestLateHelperReallyRacesTheDiagnosis`

**The social failure mode, which is the part that actually costs weeks.** The
technical defect in #338 was one missing `drain` call. The expensive part was
the label. "Flaky" is a diagnosis that *ends investigation*: once applied, the
test stops being read as a signal and starts being read as weather. It gets
applied by whoever is in a hurry, which is everyone, and it is self-sealing —
the next red confirms the label instead of challenging it.

The issue itself carried an under-powered negative in good faith: *"not
reproducible on demand"*, supported by three runs. At a 0.5% rate, three runs
had a ~98.5% chance of looking clean. `-count=400` produced two failures in
about a second. Nothing was done wrong there except sampling too little and then
believing the result — the same species as an instrument that fails silently,
except this one fails toward *"there is no bug"*, which is the direction that
closes tickets.

Two practical rules:

- **Before calling anything flaky, run it `-count` in the hundreds.** It costs a
  second and it is the difference between "not reproducible" and a failure you
  can read.
- **Never let "flaky" be a resting state.** A flaky test gets a fix or an issue
  with a named owner. The third state — known-flaky, tolerated, unowned — is
  where a real defect hides in plain sight with a green suite around it.

### 5. The test that reaches past the wire — needs to go through the dispatcher

The unit under test is fine, and the test proves it is fine. What is missing is
the *call*: nothing routes to it in production. No test of the unit can tell you
that, because every one of them supplies the call itself.

**Real case.** A TUI pane was written along with its message handlers. It
compiled and it rendered — with the async messages never routed through
`Update`, so the pane would have sat on "loading…" forever. Every unit test of
the handler passes, because the handler is correct. The wire is the part that is
missing, and the tests are structurally incapable of noticing.

This is shape 1 in different clothes — an assertion passing for a reason
unrelated to what it claims — but it earns its own line because the substitution
is so natural that it doesn't feel like one. Calling the handler directly *is*
the obvious way to test a handler.

**The control.** Assert through the dispatcher, never by calling the handler
directly. Then mutate the wire: delete the routing line and confirm the test
goes red. If it stays green, the test is measuring the handler while you are
reading it as a statement about the feature.

→ from d3; both mutations lethal on the pane as landed.

### 6. Two guards that are each correct — needs someone to mutate the OTHER one

Both statements are true and the pair is still not a guarantee.

**How it happened.** The website's Windows page and the binary must not tell an
operator two different ways to install WSL. d5 built
`scripts/site-wsl-hint-check.py`, which asserts the page's install block matches
`wsl.InstallHint`, read out of `internal/wsl/wsl.go` at check time rather than
copied. Sound, and mutation-tested four ways. In the constant's doc comment, d1
then wrote that the website is checked against it.

Neither was wrong. The check really did compare the page to the constant. The
comment really did describe a check that existed. And the pair still guaranteed
nothing, in two directions at once:

- A step that existed **only on the page** was invisible to a page-is-a-subset
  assertion — so the one thing the check could not see was the one thing that
  had drifted. "The check passes" meant *the parts I copied agree*, not *the two
  agree*.
- Deleting the check would leave the comment asserting a guarantee nothing
  provided. And nothing ran the check: it was in neither `ci.yml` nor the
  Makefile, so two green runs by two people were both real and neither was
  repeatable by anyone else.

**Why it is not shape 2 or 3.** Checked against both before claiming a new
section. Shape 2 is a guard with nothing to check; this one had something to
check. Shape 3 is a guard nothing can violate; this one was violable, four
mutations, all lethal, control passing either side. **Mutation-testing each
guard individually passes.** The gap is in the seam, and the seam belongs to
neither owner.

**The tell.** Two artifacts maintained by two people, each asserting something
about the other. A comment that describes a guard elsewhere; a check that reads
a constant it does not own; a doc quoting a value from source. Each author can
verify their own half completely and correctly.

**The control.** Someone mutates the *other* person's guard. Delete the check
and confirm the comment's owner has a test that fails. Add a step to the page
and confirm the check catches it. Then close the loop so the coupling is
enforced rather than described — a Go test that fails if the check file is gone,
*and* fails if the check stops mentioning the constant, because **present is not
the same as watching**: a check that had drifted onto another constant satisfies
a bare existence assertion while guarding nothing.

Two smaller traps, both live here:

- **A guard nobody runs is decoration.** Wire it into CI in the same change that
  writes it. Both authors had run it by hand and called it done.
- **Do not give it a `paths:` filter.** The obvious tidy-up is to skip the job
  unless Go or HTML changed. The commit most likely to break a page-vs-source
  check is a *page-only* edit — exactly what `paths: ['**.go']` skips — and a
  skipped job reports green.

**Why this earns a section rather than a footnote.** No amount of further care
applied to either half surfaces it. Careful checking of your own half is what
*produces* it: you verify what you control and describe the whole. The detection
method is therefore procedural rather than technical — cross-mutation is not a
step anyone reaches by being thorough about their own work, so it has to be
asked for.

→ found by d5, reviewing d1's fix for a bug d5 had reported. Both halves now
enforced; `site-wsl-hint` runs on every push.

### 7. The guard that matches TEXT as a proxy for MEANING — needs a funnel, not a longer list

Text is escapable in both directions, and a guard built on it fails both ways at
once: prose satisfies it, and a synonym evades it.

**Four instances in a single day**, across four people's work, which is what
earned it a section:

- **A claim sweep searched for wording, not the claim.** A page said "each agent
  runs in a container" — the per-agent isolation claim, made in full — and
  survived every sweep because it contained none of the words those sweeps
  looked for: no "isolated", no "walled off", no "from each other". Found on the
  third pass, by re-asking the question against a different failure rather than
  re-running the same search.
- **The coverage gate counted a mention as a test.** `cliReferenced` matches
  `dejima <verb>` anywhere in the test corpus, so a COMMENT explaining a bug
  about a command marked that command covered — and the ratchet then asked for
  the deletion of the waiver protecting it. Prose registering as coverage.
- **A source-level guard read its own explanation.** A check asserted the
  function body contained `DEJIMA_ROLE`; the body contained a comment saying why
  `DEJIMA_ROLE` is set, so deleting it from the actual command changed nothing.
- **An openapi route extractor read comments**, so a commented-out route became
  a documented one.

**The tell.** The guard's definition of the thing is a string, and the thing can
be expressed in a string the guard does not hold. Ask: *could someone make this
claim, or this call, in words my list does not contain?* If yes, the list is a
sample rather than a definition — and it will be a smaller sample every time
someone paraphrases.

**The control.** Two, and the second matters more:

1. **Strip what is not code before matching.** Comments are not calls; prose is
   not coverage. Cheap, mechanical, and it removes the false-positive half.
2. **Funnel the thing through one function, then guard the funnel.** This is the
   half that closes it. A vocabulary list catches a phrase already in it; it
   cannot catch a NEW phrase, because the person introducing one is by
   construction not editing the list. Make the claim impossible to render without
   going through `containmentClaim(level)` — or the command impossible to invoke
   without going through one constant — and the question stops being "did the
   author remember the list" and becomes "did the author render a string at
   all", which a scan can answer.

**The anti-pattern to name, because it is the tempting fix.** When the guard
fires on something legitimate, do NOT loosen the list to accommodate it. A claim
list edited to let one string through stops being a claim list. Exclude that one
case AT THE CALL SITE, by exact text, with a control that fails if the text is
ever reworded — so the exclusion cannot outlive the reason for it.

→ instances from d5 (the sweep, the exclusion pattern), d2 (the funnel argument),
d4 (the extractor), and three of d1's own in one afternoon.

## The mutation that never applied, and printed as a pass

Every shape above is found by mutating the code and watching the guard fail. So
the mutation is itself an instrument, and it has one failure mode that is worse
than all the others: **a mutation that does not apply is indistinguishable from
a guard that catches it.** Both print `ok`.

This is not hypothetical and it is not rare. In a single day:

- d1 ran three mutations against a source file holding icons as `\uXXXX`
  escapes, while the patch strings carried the rendered glyph. Nothing matched.
  All three printed `ok`, and the honest reading of that terminal output was
  "the guard caught every one" — the exact opposite of the truth.
- d2 hit it in the tooling they had built *after* their own invalid-control
  incident, specifically to prevent this: a mutation's precondition rejected the
  patch because `gofmt` had reformatted the line being matched, so the test ran
  against an unmutated tree and reported `ok`.

Both were caught only because an assertion demanded the substitution actually
land. Without that assertion, both would have been recorded as verified.

**The control: assert the mutation applied at exactly one site, and fail the run
if it did not.** Count matches before patching; refuse on zero and refuse on
more than one. Zero means the patch missed; more than one means you changed
something you were not reasoning about, and the guard may be failing for the
wrong reason — which reads as success just as convincingly.

Two smaller lessons that travel with it:

- **Never pipe the gate into anything.** d2's `&&` chain committed while `*
  gofmt: 1` was printed directly above it, because the pipeline reported
  `tail`'s exit status. The command ran, it reported the problem correctly, and
  the thing being read for pass/fail was not the thing that knew. Run it
  unpiped and check the exit code.
- **A guard you cannot see fail has not been tested**, and that includes the
  ones you wrote to test other guards. Mutation tooling is not exempt from the
  discipline it enforces; d2's instance is the proof, since it happened inside
  the fix for the same problem.

→ instances from d1 (three in one afternoon) and d2 (inside the tooling built to
prevent it).

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

### The control that passed for the wrong reason

Everything above assumes the control itself is sound. It has the same failure
mode one layer up, and this one is worth its own heading because the instinct
that catches it runs backwards from the usual one.

The three mutation traps in shape 3 are the mechanical version of this; what
follows is the same failure where no mechanical check would have helped, because
nothing was wrong with the tooling.

**What happened.** Two things were fixed at once: a wrong boolean pair, and a
hardcoded path literal that should have come from a canonical table. A mutation
of the path was expected to fail the test. **It passed.**

That first pass was not the bug — it had a real explanation. The fix had already
made the drift impossible: both sides now read one exported constant, so moving
it moves them together and there is nothing left to detect. The constant is
`api.GitHubCredentialMountPath`, and it did not exist before that change —
which is *why* the literal had been duplicated in the first place, and why the
old failure mode was real right up until it wasn't. A fix working so well that
the failure it defends against stopped existing is a *good* result, and one that
should prompt "why?" rather than a celebration.

The error was what came next. A control was built — put the old literal back,
drift the path again, expect a failure this time — and **it passed too**, and the
conclusion "the coupling is fine, the test is sound" was one sentence from being
written up. The control had reverted the literal but kept the boolean fix, so the
mismatch degraded into a different warning: one containing the exact word the
test greps for. Two things changed; the result was attributed to one.

Re-run properly against unmutated `master` in a throwaway worktree, drifting the
path *does* fail the test, in both directions.

In the words of the person it happened to: **the first pass was correct and
unexplained; the control built to explain it was the broken instrument.** And the
reason it was broken is worth more than the mechanics — *"I already believed the
answer; the control was there to agree with me. That is the state in which a
non-isolating control does its damage."*

So: **the original claim was right and the method used to confirm it was not**,
which is the combination that survives review.

**Why this needs its own rule.** A control that fails makes you look harder. A
control that *passes* makes you stop and write the conclusion. So a
non-isolating control is at its most dangerous exactly when it agrees with you —
it doesn't produce no answer, it produces a confident answer about a different
question.

> **A surprising pass deserves the same suspicion as a surprising failure, and
> reliably gets less, because it feels like confirmation.**

And the counterfactual is the part worth sitting with, in the words of the
person it happened to: *if the first mutation had failed, I would have accepted
it and moved on with an invalid method still in my hands — and used that method
again on something where nothing surprising happened to interrupt me.*

The remedy is the one this whole document keeps arriving at, applied to the
control instead of the guard: **change one thing, and prove the control can
register a failure before trusting the pass.** A throwaway worktree off an
unmutated base is usually the cheapest way to guarantee the first half.

*(Incident from d2, sent over at d1's request rather than edited in directly.)*

## The instrument was fine; the reference was not

Every shape above is a check that could not see. These saw perfectly and answered
a question nobody had asked. Nothing in the output betrays it, because the output
is *correct* — the assumption lives in the step from output to conclusion, which
happens in the operator's head where no control can reach it.

The remedy is therefore not a better instrument. It is naming the reference out
loud, before reading the result.

The entries below split into two kinds, and the difference decides what the
remedy can be. In the first three the reference **existed** and was misread: a
moved branch tip, a count standing in for an identity, a schema that shifted
under a hash. Naming it out loud is enough, because there is something to name.
In the last one there was **no reference at all** — two artifacts asserting the
same fact with nothing pointing either way — and no amount of care within either
one could have reached it. That is why its control is the odd one out: it does
not read a reference more carefully, it manufactures one that never existed.

### A diff compares against a reference, and the reference moves

**What happened.** A rebase onto `origin/master` hit one conflict in
`openapi.yaml`. It was resolved, and then the resolution was diffed against
`origin/master` to check the work. The diff showed the branch *deleting* a large
block of documentation that master had.

It had not. Master moved **twice** during the few minutes of resolving, so the
rebase had started from a tip that was already stale and the verifying diff was
comparing an in-progress tree against a master that had moved underneath both.
`git diff` behaved exactly as specified. **It renders "you deleted this" and
"you never had this" identically.**

**The tell.** A diff shows you removing content you never touched. If you cannot
point at the line in your own patch that removes it, you are almost certainly
comparing against a reference that has moved.

**The control.** `git rev-parse HEAD` and `git rev-parse origin/master` before
believing the diff, and re-fetch immediately before you **verify** — not only
before you start. At this repo's merge rate those are different moments.

**The near-miss.** One step from reporting that the rebase had eaten master's
new path-param docs — which would have sent someone hunting a data-loss bug that
did not exist. Note the direction: almost everything else in this file fails
toward *all clear*, while this one fails toward **a false alarm about someone
else's work**, the same direction as the wrong-site mutation trap and just as
hard to walk back.

*(From d3.)*

### A sequence range bounds how many things happened, never which

**What happened.** The question was whether a folder seed writes one ledger
entry *per file* or a single batch entry. It matters: `--verify` walks a hash
chain of crossings, and a batch entry carries no hash for the thing that
crossed. The evidence to hand was a gap in the sequence numbers — `port.grant`
at 1962, `port.revoke` at 1968, five files seeded. Five slots, five files.
**The number matching expectation is exactly why nobody looked harder.**

The count was right. The inference was not, and it took tracing to see why.
Between the grant and the revoke, only `trade.read` can append *from this
island* — but the ledger is **host-wide**. A concurrent append from any other
island takes a slot in that range and pushes a `trade.read` outside it. Five
slots would then hold four reads and one stranger, and the count would still
look right while the conclusion was wrong.

**The tell.** Any argument of the form *"the sequence numbers are contiguous,
therefore nothing else happened"*. Contiguity bounds the **count**. It says
nothing about the **content**.

**The control.** Enumerate the range instead of counting it. Ask the log for the
entries by type — `dejima audit --island X --type trade.read` — rather than
asking a range to imply them. The operator's run did enumerate, and every slot
came back named and sized, which *disproves* the confound for that run instead
of leaving it merely unlikely.

**The near-miss.** One step from recording "per-file ledgering is observed" on
the strength of a matching count. It happens to be true. It would have been
recorded as observed either way — and filed under *observed*, which is the word
this project spends the most care on.

> **A shared log under concurrency answers "how many", never "which". If your
> conclusion is about which, the range cannot carry it, however neatly the count
> lines up.**

*(From d3; the one-line version is d1's.)*

### The reference is another artifact, and nothing points at it

**What happened.** Dejima's containment boundary is the island, which is per
*project*: an island holds several agents, sharing it, each on its own git
worktree. The website said the wall was around each *agent*. That is false, and
it is a security claim, so it is the error a reader notices fastest.

It was corrected three times. Each time a person found it by re-asking the
question; no check ever did. The third instance is the one worth keeping,
because it explains the other two: the page read **"each agent runs in a
container"** — the claim in full, containing none of `isolated`, `walled off` or
`from each other`, which were the words every previous sweep had searched for. I
had been searching for the *wording* rather than for the *claim*, so each sweep
could only re-find what it had already found, and its clean result read as
coverage.

Meanwhile the code was right the whole time, and said so in its own tests.

**The tell.** Two artifacts assert the same fact, they have different owners and
different review paths, there is no shared test — and **neither file mentions
the other**. This is weaker than a seam and worse. A seam has two owners and a
boundary somebody drew; the control for it is that each owner mutates the
other's guard. Here nobody had drawn a boundary, so there was nothing to stand
on either side of. Product copy and product code are the instance; docs against
a config schema, or a README against a CLI's `--help`, are the same shape.
Thoroughness *within* either artifact cannot reach it, because each is
internally consistent and correct.

**The control.** Pin the claim-bearing sentences and check them in CI
(`scripts/site-claim-check.py`). Not a banned-phrase list: a list goes quiet
exactly when a new wording appears, and whoever writes the new wording is by
construction not editing the list. Pin what the sentences *say*, so a rewording
fails until a human looks. Not a golden over the page either — these are
marketing pages, mostly prose, and a golden fails on every legitimate edit, which
is how a guard gets switched off rather than fixed.

**Pin the count, not the presence.** Two of the eight pinned sentences appear
*twice* — once in FAQPage JSON-LD, once in the visible answer. Presence alone
passes a half-fix, and a half-fix is not hypothetical here: correcting the
visible copy and not the structured-data copy is how this claim stayed live for
days after it was "fixed", where search engines and models read it.

**And say what it cannot do.** It catches **drift in known claims, never
invention of new ones.** A new page making the same claim in fresh copy passes
untouched, because nothing forces new prose through the manifest. A renderer can
be given a funnel — one function every claim must pass through — and a page
cannot; there is no single call site for a sentence. The person re-asking the
question is still load-bearing. This only puts a floor under them.

**The near-miss.** The first mutation written against the mirrored pair
**matched nothing** — the surrounding markup had been guessed. Without the
precondition asserting that a mutation applied, a no-op would have printed as a
passing test, and the guard would have been credited with catching a half-fix it
had never been shown. That trap is already recorded in this file for Go and for
YAML; a third format makes it a property of mutation testing rather than of a
language. **Assert the mutation applied before you trust what the test says
about it.**

> **When two artifacts assert the same fact and neither one names the other,
> there is no seam to check — so the check has to manufacture one.**

*(From d5.)*

### A field added to a hashed struct rewrites history unless it omits when empty

**What happened.** The audit ledger hash-chains each entry by marshalling it, so
the serialised bytes *are* the reference. Adding a `Provenance` field to that
struct — brokered / witnessed / self-reported, so a self-reported row can be told
from a brokered one — changes the marshalled form of every row. Without
`omitempty`, the new field serialises on historical entries too, every chain
value shifts, and `dejima audit --verify` fails on a ledger nobody has touched.

**The tell.** Verification fails on a file you did not edit, immediately after a
schema change. If the only thing between the last green verify and this red one
is a struct field, suspect the serialisation, not the data.

**The control.** A test asserting an unstamped entry does not serialise the
field at all. That is the whole guarantee: the zero value must be *absent*, not
empty-but-present, or the bytes differ.

**The near-miss.** `omitempty` reads as a style choice on almost every other
struct in the tree, which is exactly why it is easy to drop in review. Here it is
load-bearing. And note the direction, which is the same as the moved-reference
cases above and the reason this sits with them: the failure does not read as "we
changed a struct". It reads as **tampering** — the single most alarming and least
informative thing an audit tool can say — and it points the reader at the ledger
file rather than at the commit that actually caused it.

> **If a struct is hashed, its schema is part of the reference. Any field that
> can be empty must be absent when it is, or you have rewritten history.**

*(From d3.)*

## A verification has a timestamp; a gate does not

Those above are references that were wrong at the moment they were read, or
absent entirely. This one is a reference that was right, and then moved: a check
that saw correctly, and then the thing it saw changed.

**What happened.** Before adding a field to an API type, I checked whether
`openapi.yaml` needed an entry. Two pieces of evidence, both verified rather
than assumed: the parity gate was route-level, not field-level; and sibling
fields on the same struct (`built_version`, `never_heard_from`) were not
documented either. Conclusion: no entry needed.

Both halves were true when I checked them. Between that check and the push,
another agent's audit documented thirty missing fields — including the two I had
reasoned from — and added a field-level gate. **The precedent I had verified was
the drift**, and it was fixed underneath me. CI failed, correctly, on a
conclusion that had been sound an hour earlier.

**Why this isn't "verify harder".** There was no sloppiness to remove. The check
was right; its subject moved. Re-running it more carefully, or later, or twice
(the resampling rule above) would only have narrowed the window, not closed it —
in a repo with several agents committing, *any* verification can be invalidated
between the check and the merge.

> **A verification is a statement about a moment. A gate is a statement about
> every moment after it.**

That is the argument for spending effort on gates rather than on vigilance, and
it was made here by a gate catching what a careful, correct, well-evidenced
verification could not. If you find yourself reasoning from "the existing code
doesn't do X either", notice that you are reasoning from a *precedent* — which is
someone else's decision, revisable at any time, and not a rule until something
enforces it.

The practical form: when a check's conclusion is "no change needed", ask whether
anything would *tell you* if that stopped being true. If the answer is no, the
conclusion has a shelf life, and the cheapest fix is usually to make the rule
enforceable rather than to remember it.

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
| `internal/api/background_join_wiring_test.go` | `TestJoinWiringGuardRecognisesAnUnwrappedCall` + `seen == 0` fatal | the matcher still fires, AND it is still matching something |
| `cmd/dejima/background_join_test.go` | `seen == 0` fatal | the guard is still reading the package it guards |
| `internal/wsl/wsl_test.go` | the `socat-missing-late` fixture + `TestLateHelperReallyRacesTheDiagnosis` | the race is forced rather than hoped for, and the fixture still forces it |
| `sdk/openapi_field_parity.py` | `--self-test` (three mutations) + five `MIN_*` floors | the check still sees a field removed from either side, AND it is still binding types and reading routes rather than scanning an empty set |

## The one-line version

**Prove the check can see a failure before trusting its silence** — and when it
does speak, **name what it was comparing against before trusting the answer.**
