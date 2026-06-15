# Decision memo — capability brokering (Shortcuts/Notes vs files-only)

**Status: RATIFIED 2026-06-15.** Resolves open question
[`port-island-spec.md` §10.4](port-island-spec.md).

> **Decision (maintainer):** **Fast-track a pragmatic Option C *now*** — a narrow,
> typed capability broker — rather than holding files-only (A) through dogfood.
> Rationale: the brains Dejima targets (OpenClaw, Hermes, Letta) are
> function-calling agents that need *structured tool calls*, not just a
> filesystem, to be useful. **Option B (general host-command broker) is a
> non-starter — permanently rejected.** Architecture: a daemon endpoint
> `POST /v1/capabilities/execute` taking a named target + a string→string arg map,
> mapped by per-platform adapters — **macOS → Apple Shortcuts**, **Linux →
> user-authored scripts in `~/.dejima/capabilities/`**. Full design in
> [`capability-broker-spec.md`](capability-broker-spec.md). The analysis below is
> retained as the reasoning of record; §A "skip" is superseded by the decision to
> build C now (the recommendation that follows was the pre-ratification view).

---

## The question

The Port brokers **files** — byte flows in/out under per-island scope, every
operation a typed entry in the hash-chained Ledger (§3.4, §4). It deliberately
does **not** broker **host-OS capabilities**: firing a macOS Shortcut, appending
to Apple Notes (an API, not a file), sending iMessage, AppleScripting an app.

That gap matters specifically for the **24/7 personal-assistant** use case
(positioning.md): a contained brain can read your Obsidian vault (files) but
cannot "remind me at 5", "text Sam I'm running late", or "append this to my
Notes inbox" — because those are capabilities, not files. Today's answer
(§3.2 escalation) is honest but blunt: **needs host-OS capabilities → run
native (uncontained), and we say so plainly.** That trades away the containment
thesis for exactly the user who most wants an always-on assistant.

So: do we ever broker host capabilities, or hold the files-only line forever?

---

## Why §3.4 drew the line where it did

The objection in §3.4 is precise and correct: a ledger of **arbitrary host
commands** is intractable. "The agent ran some command with some effect" can't
be audited — you'd be logging arbitrary side effects, and the Ledger's value
(tractable, complete, replayable) collapses. A *file* ledger is tractable
because every entry has the same shape: path, direction, island, agent, bytes,
hash, time.

The key realization: **that objection is to *arbitrary* commands, not to
capabilities as such.** A *curated, typed, per-capability* broker is as
auditable as the file broker — because each capability type carries its own
fixed ledger schema.

---

## Options

**A — Files-only, permanently.** Hold the line; capabilities are forever
out of scope. Assistant users who need host actions run native (uncontained).
- *Pro:* simplest; thesis stays crisp; zero new audit/security surface.
- *Con:* the flagship assistant either stays half-useful while contained, or
  goes uncontained — the containment value prop evaporates for that user.

**B — General host-command broker.** A configurable allowlist of host commands
the Port runs on an island's behalf (`port run <named-cmd> [args]`).
- *Pro:* maximally flexible; one mechanism covers everything.
- *Con:* this is exactly the §3.4 trap. "Allowlist of commands" still means an
  arbitrary-effect ledger (you log the argv, not the effect), a large security
  blast radius (each command is a potential escape), and an audit story that
  doesn't actually tell you what happened. **Recommend against.**

**C — Narrow, typed capability adapters.** A small set of **first-class,
individually-audited capability types**, each with its own grant, schema, and
ledger event — modeled exactly like file trades, not like a shell:
- e.g. `capability.shortcut` (run a *named, pre-granted* macOS Shortcut with a
  string arg), `capability.note-append` (append text to a *named* Notes note),
  `capability.notify` (local notification). Each is a typed operation with a
  fixed ledger schema (`{type, island, agent, target-name, args-hash, time}`) —
  tractable and complete, like `trade.read`.
- Gated like Port scopes: **per-island, per-capability, explicit grant**
  (`dejima cap grant <island> shortcut:MorningBrief`), deny-all default,
  revocable, fail-closed, ledgered.
- macOS Shortcuts is itself a user-curated indirection layer (the user defines
  what "MorningBrief" does), which makes it the natural *first* adapter: the
  blast radius is bounded by what the user already chose to expose as a Shortcut.
- *Pro:* keeps the tractable ledger; contained assistant can act; each adapter
  is small and reviewable; the line against *arbitrary* commands (B) still holds.
- *Con:* N adapters to build/maintain (one per capability); macOS-specific code;
  reachability couples to the same in-island→host path as autonomy (§3.2/#8).

---

## Recommendation

**Hold files-only through v1 + dogfood (A for now); when demand is proven,
add narrow typed adapters (C) — never the general command broker (B).**

Concretely:
1. **Don't build it yet.** Files-only stays the v1 line. It keeps the thesis
   clean while the Port's read/write + autonomy paths get field-proven.
2. **Let the dogfood (#14) name the capability.** Ship the assistant on files +
   native-escalation, and record which host action you *actually* reach for
   first. Don't speculatively build adapters no one needs.
3. **When one capability clears the bar, build it as a typed adapter (C),**
   almost certainly `capability.shortcut` first (Shortcuts is a user-curated
   allowlist by construction). One adapter, one grant verb, one ledger type —
   reviewed like the Port was.
4. **Never B.** Record any pressure toward a general command broker here and
   reject it; it reopens the §3.4 trap.

This makes the residual-risk choice explicit (§3.2) *and* gives a contained
path forward, without betting v1 on a large macOS-specific surface.

---

## What ratifying this changes

- §3.4 / §10.4 move from "open: go/no-go" to "decided: **typed adapters yes,
  general command broker no; sequenced post-dogfood.**"
- Adds a future `dejima cap grant/revoke` surface mirroring `dejima port`, and
  `capability.*` ledger event types, when the first adapter is built.
- Couples to the autonomy path (#8): a capability fired by the in-island brain
  rides the same authenticated in-island→`dejimad` channel.

> **Decision needed:** ratify "A-now / C-later / never-B", or pick a different
> stance. Until ratified, the code stays files-only (the safe default).
