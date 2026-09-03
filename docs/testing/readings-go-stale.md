# A reading that was true when you took it looks exactly like one that is true now

[`guards-need-controls.md`](guards-need-controls.md) is about whether a check has
a **subject** — whether the thing you are asserting on can still see a failure.
This file is about something that happens after that question is settled: a check
that ran correctly, on a real subject, and produced a **stale** answer that is
indistinguishable from a current one.

Nothing here is a wrong answer. Every instance below is a *correct reading of a
world that has since moved*, or a correct-looking reading of a world that was
never what the instrument described. There is no error text, no red, and no
moment where anyone was careless.

## The rule

**Ask for the thing that cannot be stale.**

Every instance was fixed the same way, and the fix is always cheap — a second
field in a call you were already making, a flag, a hash:

| you were reading | it can be stale because | ask for |
| --- | --- | --- |
| CI check conclusions | they belong to a *ref*, not to the PR | `state` (OPEN/MERGED/CLOSED) |
| `mergeable` | a merged PR stops computing it, forever | `state`, in the same call |
| `go test` output | Go caches a pass and reprints it | `-count=1` |
| "fixed in #387" | a PR can be open, merged, or reverted | the tag, or the file at that ref |
| a version claim | master is not what anyone runs | `git tag --contains <sha>` |
| a `$?` after a job | the job may not have finished | the thing the job wrote |

And the second half, which no discriminator can supply:

**State the as-of.** A correct reading with no timestamp becomes a false claim
the moment the world moves, with nothing in the sentence to warn the reader. It
does not need ceremony — "as of `<sha>`", "at 20:58Z", "on the tip at merge
time". One clause.

## The instances

Eight, across five people and four different surfaces — git, CI, the Go
toolchain, and our own messages. None of them is a test.

### 1. A cached `go test` pass, of a mutant that never ran

A mutation was applied, `go test` printed `ok (cached)`, and the mutation was
recorded as survived. It had not run at all: the cache replayed the previous
tree's result. Re-run with `-count=1`, it survived *for real* — and the guard it
survived turned out to be hollow, so the cached reading was a false negative
wrapped around a true one.

**Ask for:** `-count=1` on every mutation run. A cached pass is a pass of
whatever ran last time.

### 2. `mergeable: UNKNOWN` on a PR that was already merged

GitHub computes mergeability asynchronously, so `UNKNOWN` normally means *still
working*. On a **merged** PR it stops computing and the field stays `UNKNOWN`
permanently — beside the seven green checks from the last run before the merge,
which read exactly like "ready to merge". Three PRs were waited on patiently
that had landed hours earlier.

**Ask for:** `state` in the same call. `MERGED`/`OPEN`/`CLOSED` is authoritative
and instant.

### 3. CI greens that belonged to a branch, not to the PR

A PR was merged and closed. Five further commits were pushed to its branch. The
branch's checks went green, `gh pr view` reported them, and they were read as
the PR's — but a merged PR does not reopen when its branch moves, so those
commits were attached to nothing and nobody was ever going to merge them.

This is instance 2 seen from the other side: the same `UNKNOWN` was on screen
every time, and it was read as lag.

**Ask for:** `state`, before waiting on anything else.

### 4. "Corrected in #387", read as landed — twice

A fix described as done was sitting in an unmerged PR. The published file was
still wrong. The person who went to check found it wrong both times; anyone who
did not would have carried the correction as fact.

**Ask for:** the file at the ref you mean, or the tag. "In #387" describes a
proposal; only a merge commit or a tag describes the world.

### 5. CI greens from a 165-commit-old tree

A PR sat long enough that its last green run predated two CI jobs that did not
exist when it ran. The greens were real and answered a question about a tree
nobody had any more.

**Ask for:** the merge result, not the branch — merge the base in and re-run
before reading. *"My branch is green"* is not *"the merge is green"*.

### 6. `git describe` read before the merge it was describing had landed

A version string taken a moment too early, reported as the version the change
shipped in.

**Ask for:** `git tag --contains <sha>` — a question about a specific commit
rather than about "now".

### 7. `setsid go test; echo $?` — a measurement reported before it finished

`setsid` **forks**. The shell gets `setsid`'s exit status, which is 0 as soon as
the child is launched, while the tests are still running; a following `grep`
then reads a log file that has not been written yet. Every part of the pipeline
worked. The answer described a moment before the question was answerable.

**Ask for:** the artifact the job produces, not the exit status of the thing that
started it. This is the purest member of the family — not a stale reading of an
old world, but a reading taken before the world existed.

### 8. A correct measurement that the world invalidated twenty minutes later

*"`v0.8.99` is the newest tag; master is 47 commits ahead; none of today's work
is released."* Measured, not guessed — tags listed, SHAs checked individually,
count taken. It was right when taken and wrong when read: `v0.9.0` had been cut
in between.

There is no discriminator for this one. It is the residue the rule cannot
remove, and it is why the as-of clause is half the rule rather than a decoration.

## Why this is hard to see from inside

The other document's failures feel like nothing happening. **These feel like
success.** You asked a precise question, an instrument answered it precisely, and
the answer was true. Every instinct that catches a wrong answer — re-read it,
check the logic, ask someone — returns the same result, because the reading is
not wrong. It is *old*, and age is the one property the reading does not report
about itself.

That is also why it recurs across surfaces that have nothing to do with each
other. It is not a property of Go, or GitHub, or git; it is a property of asking
a system for a status and receiving a value with no timestamp attached.

## Related shapes — fresh readings that answer the wrong question

Two cases arrived while this was being written that are **not** members, and
saying why sharpens the family rather than widening it. In both, the reading is
current, the instrument is fine, and nothing has expired. What has gone wrong is
that success stopped discriminating.

### A remedy that manufactures the precondition the bug needs to hide

To unblock a Windows operator whose client could not reach its WSL daemon, the
proposed workaround was:

```
ln -sf /root/.dejima/dejimad.sock /.dejima/dejimad.sock
```

It puts the real socket where the broken dial looks, so it works. It also creates
a real socket at **exactly the path an unfixed client computes** — so with it in
place a fixed client and an unfixed client both connect. The operator would have
installed the new build, watched it work, and confirmed nothing; we would have
recorded a field verification against a fixture that makes the bug invisible.

The operator refused it: *"I am looking to test dejima solutions, not just get it
working… feels like cutting the corner."* Instinct, not method — which is the
same thing this file keeps finding.

**Not a stale reading:** it never had a moment of being true. The subject was
altered so that a pass no longer distinguishes fixed from unfixed. That is closer
to [`guards-need-controls.md`](guards-need-controls.md) — a check that cannot
fail — except the thing removing the failure mode is a *remedy*, applied
deliberately, by the person doing the verifying, on the one run that mattered.

### A predicate that is a proxy for the question

`dejima wsl status` printed four green checks and "ready" for a daemon that had
been dead for thirty hours, because the socket check was `[ -S ]`: **existence,
not liveness.** A precise, current, correct answer to a question nobody meant to
ask.

**Also not stale**, and the discriminator for this one is different again: ask
whether the predicate can distinguish the two states you care about. A socket
file survives the process that created it, so `[ -S ]` cannot tell running from
dead — and neither can any amount of re-running it.

### If this grows

Two instances is an observation. If a third arrives, this wants its own file
rather than an annex to a family it is not part of — the discriminators are
genuinely different (*can this predicate distinguish?* rather than *is this
reading current?*), and forcing them together would blunt both.

## What this does not cover

Not every stale claim is this shape. A claim that was **never** true is an
ordinary error and belongs in a postmortem. A guard that cannot see a failure at
all belongs in [`guards-need-controls.md`](guards-need-controls.md). This file is
only for the case where the instrument worked, the reading was right, and the
sentence built on it stopped being true without anyone touching it.

*(Assembled by d2 at d1's request. The instances are d1's, d3's, d4's, d5's and
mine; the shape only became visible because most of them were not mine to own.)*

*One note on how this was possible, because it is the practice worth copying
rather than the doc. Four of these are mine, from a single afternoon. What made
them usable was not making the mistakes — it was that each one had been written
down AT THE TIME, in a message or a commit, before anyone understood what it
was. A postmortem written after you understand the shape cannot recover the
detail that identifies it; the receipt written while you were still wrong can.
d3's phrasing, and it is the better half of the point I originally made.)*
