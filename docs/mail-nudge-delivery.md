# Delivering a mail nudge: two attempts, both withdrawn

**Status:** delivery is UNCONDITIONAL today (`internal/api/wake.go`). This file
exists so the next person to try does not re-derive the measurements or repeat
the mistake.

## What the nudge is

When mail lands in an island's mailbox, the daemon types a one-line notice into
the recipient agent's tmux pane and presses Enter:

```
📬 2 new message(s) — run: dejima msg poll
```

The nudge exists only to WAKE an idle agent. The mail itself is already in the
mailbox and `dejima msg poll` reads it whenever the agent gets there.

## The complaint that started it

A notice arriving while the operator was **mid-sentence** landed inside their
half-typed message and submitted the result. tmux types at the cursor; Enter
sends the whole box.

The existing turn-boundary gate does not help. It asks whether the AGENT is
idle, and while a human types the agent is `waiting-for-input` — idle by that
measure, at exactly the wrong moment.

## Attempt 1 (#438): paste instead of submitting — WITHDRAWN

Bracketed paste puts text in the box without submitting, so the draft survives.
Delivery became: submit when the box is empty, paste when it is not.

**Why it failed.** A paste is only delivered when a human presses Enter — and it
was chosen only when nobody had typed for 45 minutes. The mode built for *"the
operator is away"* was the one mode that cannot work while they are away. Mail
sat in input boxes indefinitely.

The operator's verdict was that it was worse than the always-submit behaviour it
replaced, and that is the right call: **mail that silently never arrives is worse
than mail that arrives at an awkward moment.** One is unrecoverable and invisible;
the other is annoying and leaves the text in the transcript.

## Attempt 2 (#443): submit unless someone is actively typing — WITHDRAWN

Keep the hold, but only while a human is demonstrably at the keyboard, using
tmux's `client_activity` as the presence signal.

**Why it failed, and this is the useful part.** `client_activity` is real and it
does track input — but a tmux CLIENT is not a person. Measured on a live fleet:

```
31 clients across 3 sessions
oldest idle 293871s  (3.4 days)
newest idle 27s
```

Every reconnect leaves a client behind and none ever die, so "a client is
attached" is true essentially always. Presence was inferred from a proxy for
presence, and the proxy said yes for terminals nobody had looked at in days.

This is `readings-go-stale.md`'s socket case with a different subject: **existence
is not liveness.** A socket file outlives its process; a tmux client outlives the
human. Both look like evidence and are not.

### What was measured, and is still true

Against Claude Code v2.1.273, driven in a real tmux pane. Worth keeping because
it took a while to establish and any future attempt needs it:

| | |
|---|---|
| bracketed paste | never submits; text lands and stays |
| leading `\n` | inserts literally — the operator's line survives, notice gets its own row |
| trailing `\n` | leaves their cursor on a fresh line below |
| 3+ line paste | collapses to `[Pasted text #1 +N lines]`, inline, absorbing the leading newline |
| `Ctrl-U` on a 2-line draft | **JOINS the lines** instead of clearing — corrupts the draft |
| input row when nobody types | stable; no shimmer from spinners or token counters |
| cursor column on an empty box | `2` — and also `2` for a 2-line draft with the cursor at Home |

That last row is why cursor position cannot be used to detect an empty box, and
the `Ctrl-U` row is why "save the draft, clear, submit, restore" is not viable:
the clear step is destructive and the screen cannot distinguish a hard newline
from a soft wrap, so the save step is lossy too.

## The better signal, not yet built

Both attempts asked *"is a human present?"* — which nothing outside the agent can
answer. The question that CAN be answered is *"is this draft being worked on?"*,
and the evidence is the draft itself:

1. Read the input box.
2. Empty → submit.
3. Drafted → read it again after ~60s. **Changed** → someone is typing, hold and
   retry. **Unchanged** → submit.

This observes the thing directly instead of inferring it from a proxy, which is
the specific error both attempts made. Its own failure mode is someone composing
slowly who pauses for a full minute — and being wrong there costs a clobbered
draft, not a lost message, which is the right direction for the cost to fall.

**Whatever is built, the invariant is:** every path must end in delivery or a
retry. Attempt 1 shipped a state that did neither.

## The structural fix, which is better than any of this

Stop using the input box as a delivery channel. Claude Code hooks support
`asyncRewake` — a `Stop` hook can wake an agent with a message, no keystrokes
involved — and Dejima already installs `Stop` hooks in every island
(`image/agents/claude-code/hooks/`). That delivers to agents that are *working*
with no tmux at all, leaving the pane only for waking genuinely idle ones.

Codex has a first-class equivalent: `codex queue --thread <id> --message <text>`,
which never touches the input box. Its open question is addressing — `codex
agents` has no `--json`, so there is no non-interactive way to discover a thread
id; naming the session at launch is the likely answer.

Both are read off documentation and manifests, **neither verified**. Verify before
building.
