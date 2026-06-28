# Competitive drift-checker — design (v0.7.x)

A contained Dejima agent that keeps the comparison pages accurate as competitors
change. It re-verifies the cited facts on a schedule and flags drift for human
review. It never auto-publishes. a3 stays the source of truth for competitor facts.

Built as a Dejima agent on purpose: a contained, audited, autonomous
competitor-monitor that opens draft PRs is a showcase of the platform itself.

## What it is

A headless Dejima agent in a dedicated Home Island ("watchtower"). On each run it:

1. Reads a manifest of `{comparison page -> cited source URLs -> last-verified facts}`.
2. Re-fetches each cited source (through the egress proxy).
3. Detects drift: source changed materially vs the last snapshot, or the page's
   claims no longer match the source.
4. On drift: `dejima msg send --to a3` with a summary, and opens a DRAFT PR with a
   dated delta. No drift: records a "checked, no change" timestamp in the manifest.

## What the OPERATOR must create (agents can't self-spawn)

I build what runs in the island; the operator stands up the island + grants. The
watchtower runs a **Claude Code agent** — chosen because its harness already has a
native scheduler (see Scheduling), so it self-paces with no host timer:

```
dejima home create --island watchtower            # a contained home island
dejima agent add watchtower --type claude-code     # the checker's brain + scheduler
# grant it, scoped and minimal:
#  - a per-island GitHub identity scoped to the site repo (push a branch + open a draft PR)
#  - its own LLM provider key (normal agent provisioning; the agent IS the checker)
#  - an egress allow-list = cited competitor domains + github.com + api.github.com only
```

The containment story is the point: every outbound fetch is on an allow-list and
ledgered (pairs with the egress gate), and the GitHub identity can only touch the
site repo. A competitor-monitor you can prove the boundaries of.

## What I build: `tools/drift-checker/`

Not a separate program — the Claude Code agent IS the checker. I ship:

- `manifest.json` — per comparison page: the cited URLs and a fingerprint of the
  claims last verified (hash + date). Seeded from the 5 current pages' sources.
- `SKILL.md` — the drift-check the agent runs each wake: fetch each cited URL,
  judge whether it still supports the recorded claims, and on drift build a dated
  delta, open a DRAFT PR, message a3, then schedule the next run.

The agent does the fetching, the judgment (it's an LLM), the draft PR (`gh`), and
the messaging (`dejima msg`) itself. It proposes; it never edits live pages. a3
verifies the new facts first; then I finalize the page.

## Scheduling — lean on the agent's own scheduler, and build Dejima's

Agent runtimes already have scheduling. Claude Code's harness has a native cron and
scheduled-wakeup (this very runtime exposes `CronCreate` / `ScheduleWakeup`). So the
watchtower schedules its OWN monthly run — no host launchd/systemd timer, no
external waker. The agent runtime is the scheduler.

One honest tradeoff: an agent's internal cron only fires while its container is
running, so this keeps the island resident between runs. For a monthly task that's
a sleeping agent at near-zero cost. True hibernate-between-runs needs a wake signal
from OUTSIDE the container, which the agent can't give itself.

That outside signal is the **Dejima capacity worth building**: a daemon-level
scheduled-wake. The daemon is always on and already does idle-hibernate; a symmetric
"wake island X at time T" (a per-island schedule / `dejima wake --at`) lets the
platform wake a fully-hibernated island on a cadence — and it covers headless agents
too, which have no Claude-Code-style harness. File this with the backend (a1/d5) as
a platform primitive.

### Durability (the gotcha that would silently break it)

The harness scheduler is short-lived: `CronCreate` auto-expires after ~7 days and
`ScheduleWakeup` is clamped to <= 1 hour. A single "monthly cron" would stop firing
after a week and the watchtower would go quiet with nobody noticing. So v1 is
**self-sustaining**, not a one-shot long cron:

- The agent keeps `state.json` (last-run timestamp + a heartbeat written every wake).
- Each wake it re-arms a fresh short wakeup, and only runs the full drift-check when
  `now - last_run >= 30d`; otherwise it just heartbeats and re-arms. The cron is
  re-created every cycle, never relied on to live for a month.
- The heartbeat makes a stalled watchtower detectable: if `now - heartbeat` exceeds
  a threshold, something's wrong. (Wire to a Dejima zero-heartbeat signal / doctor
  if available; otherwise a3 notices the absence of monthly reports.)

Plan: ship v1 on the agent's own self-re-arming schedule (resident, no host
dependency); when the daemon scheduled-wake lands, switch to hibernate-between-runs.

## Guardrails

- DRAFT PRs only. Never merges, never edits live pages directly. Human + a3 in the loop.
- a3 = source of truth: drift -> a3 verifies the new fact -> d6 finalizes/publishes.
  The agent proposes; it does not decide competitive facts.
- Egress allow-list + audit ledger: only the manifest's cited URLs, every fetch
  logged. No crawling, no scope creep.
- The "verified <month year>" stamp on a page only moves via a merged PR, after a
  human confirms.

## Division of labor

- a3: owns competitive facts + the manifest's known-facts column.
- d6 (me): builds and maintains the checker program + the comparison pages.
- Flow: checker detects drift -> msg a3 + draft PR -> a3 verifies -> d6 finalizes.

## Build plan (after design sign-off)

1. `tools/drift-checker/` program + seeded `manifest.json`.
2. Operator runbook (the `dejima home create` + the three grants).
3. Manual dry-run once (report only, no PR) to validate drift detection on the 5 pages.
4. Wire the self-hibernate + host waker.
5. Follow-up: propose the native scheduled-wake daemon primitive.
