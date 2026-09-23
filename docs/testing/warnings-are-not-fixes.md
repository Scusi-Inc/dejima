# If the code knows enough to warn, it knows enough to offer the fix

Three times in one week, Dejima detected a problem precisely, described it
accurately, and left the operator to act on it. All three times they did not —
not through carelessness, but because a warning arrives at the moment someone has
the least context to price it, and asks them to do the work the code had already
done.

Its siblings ask whether a check has a subject
([guards-need-controls](guards-need-controls.md)) and whether a reading is still
current ([readings-go-stale](readings-go-stale.md)). This one asks a different
question about a check that is working perfectly:

> **The code got the diagnosis right. What did it do with it?**

## The rule

**A warning that names a condition the code can act on is half a feature.** Ship
the other half: do it, or offer to, or name the exact command. "Correct and
unactionable" is not a neutral outcome — it transfers the risk to the person
least equipped to carry it, and it reads as diligence while doing so.

The test is one question: *when this fires, does the operator know what to type?*
If the answer is "they'll work it out", they will work out something, and it may
be `dejima reset`.

## The instances

### 1. "Restart the agent to pick it up" (#451)

`dejima secret set` printed:

> ⚠ RESTART TERMINALS TO APPLY
> It's live in NEW shells; anything already running still has the old
> environment. **Restart the agent to pick it up.**

Correct in every particular. It named a verb and no command.

An operator set a `GH_TOKEN`, followed that instruction, went looking for *how*,
found `dejima reset`, and lost **every Codex conversation in the island**. Reset
destroys the home volume.

Nothing else was broken. `reset` warned, listed "all conversation history", and
required the island name to be typed. `dejima reset --help` names the right
command — and nobody reads the help of the command they are about to *not* use.

**The fix was four lines**: print `dejima agent restart <island> <agent> --resume`,
and say what `--resume` buys. The command existed the whole time.

### 2. Reset warned thoroughly and destroyed anyway (#452)

Every guard around `reset` worked. It warned, it enumerated what dies, it made
the operator type the island name, and `dejima eject --include-home` would have
saved them.

Every one of those asks the operator to **already know what they are about to
lose and to act before they lose it** — which is exactly the knowledge they do
not have at that moment. The handler, meanwhile, knew precisely which volume it
was about to destroy.

**The fix**: take the copy. `reset` snapshots the home volume first, `dejima
restore` puts it back, and a reset that cannot snapshot does not happen. The cost
is one volume copy; the thing it replaces is the word "irreversibly".

### 3. "Nothing here edits it" (#454)

A local reinstall on a machine that had been a client. The installer detected the
stale `DEJIMA_HOST` in `~/.zshenv`, named the file, named the consequence, and
said:

> A local daemon will start, but the CLI will keep talking to that server until
> you remove the line. **Nothing here edits it.**

That sentence was written as a *virtue*, and the reasoning behind it is sound: an
rc file is the operator's, and a sed through it is how an installer eats a line
somebody wrote by hand. But it is an argument about **editing**, and it was doing
duty as an argument about **doing nothing** — which are not the same, and only
one of them was examined.

The install then reported `FAIL` for a daemon that was fine, because its own
verification was probing the remote host too. The operator's next three commands
all failed against a machine on someone else's network.

**The fix**: offer. Prompt, back the file up, comment rather than delete. And
unset the variable for the installer's own process, so the verification checks
the thing it just built.

## What the three have in common

| | the code knew | it did | the operator did |
|---|---|---|---|
| secret set | which command applies a secret | named the verb | ran `reset` |
| reset | which volume it was about to destroy | warned | lost the volume |
| install | which file held the stale host | named the file | got a broken install |

In each case the information needed to fix the problem was **already in the
function that printed the warning**. Nothing had to be looked up, asked, or
inferred. The gap was never knowledge; it was a decision not to act on it.

And each warning was *good*. Specific, accurate, non-alarmist, naming the file or
the consequence. Quality of wording is not the axis — all three would survive any
review that asked "is this message clear?"

## Why it keeps happening

**It looks like restraint.** Not touching the operator's files, not assuming what
they want, not being clever — these are real virtues, and they are the exact
shape of the mistake. "Nothing here edits it" was written as a boast.

**The author is the one person who cannot feel the gap.** They know the remedy —
they just typed it into the warning. Reading it back, the next step is obvious,
because they already know it.

**It fails on someone else's time.** The warning prints; the install "succeeds";
the loss happens minutes or days later, somewhere the warning is no longer on
screen. Nothing connects the two, so nobody files it as one bug. Two of the three
above were reported as *"is dejima causing this?"* rather than as a defect.

## Applying it

When adding a warning, ask in order:

1. **Can the code just do it?** Reversible, obvious, no judgement required —
   then do it and say so. The snapshot in `reset` is this: nobody wants a reset
   that skips the copy.
2. **Is it the operator's call?** Then *offer*, with consent and a way back — the
   rc-file edit prompts, backs up, and comments rather than deletes.
3. **Is it genuinely theirs alone?** Then name the exact command, with the flags
   that matter. `dejima agent restart <island> <agent> --resume` — not "restart
   the agent".

Only (3) is a warning, and even (3) is a command the operator can paste.

**And if it fails closed, say what survived.** A refusal that does not state the
system is intact reads as a second failure: "refusing to reset: could not
snapshot the home volume first. **Nothing was destroyed.**"

## What this does not say

Not every warning needs an action. A warning about something the code genuinely
cannot fix — a token that expired, a VM the operator must resize, a service only
they can restart — is doing its job by being accurate and specific. The rule is
narrower than it sounds:

> **If the remedy is already in the function that prints the warning, printing it
> is not the deliverable.**
