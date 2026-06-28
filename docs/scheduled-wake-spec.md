# Spec: daemon-level scheduled wake (for a1 / d5)

A small platform primitive: let the daemon wake a hibernated island on a schedule.
Symmetric to the idle-hibernate the daemon already does. Filed at a3's request;
the first consumer is the competitive drift-checker watchtower, but it's general.

## Why

Agents with their own harness (Claude Code) can self-schedule, but only while their
container runs, so they can't be hibernated between runs. Headless agents (OpenClaw,
SDK workers) have no scheduler at all. Both need a wake signal from OUTSIDE the
container. Today the only options are a resident sleeping agent or a host-side
launchd/systemd timer. A daemon-level scheduled wake removes both crutches:
hibernated islands wake on a cadence, do their work, hibernate again.

## Shape (proposal, not prescriptive)

- Per-island schedule, set via CLI + API:
  - `dejima wake <island> --at <RFC3339>` — one-shot scheduled wake.
  - `dejima wake <island> --every <duration>` (e.g. 720h) — recurring; or a cron
    expression if that's cleaner with existing infra.
  - `dejima schedule list|rm <island>` to inspect/cancel.
- Stored with the island's state (survives daemon restart — this is the whole point;
  unlike harness cron it must be durable).
- The daemon's existing tick loop (the one that does idle-hibernate) checks due
  schedules and calls the same wake path a manual `dejima wake` uses.
- On wake, the island's agent runs as normal; it (or the daemon, configurable) can
  hibernate again when idle. Pairs with idle-hibernate: scheduled wake brings it up,
  idle-hibernate puts it back.
- Ledger every scheduled wake (it's a lifecycle event; fits the existing audit/event
  stream — emit `island.woken` with reason=scheduled).

## Scope / non-goals

- Not a general job scheduler; just "wake island X at/every T." What runs on wake is
  the agent's business.
- Minute-level granularity is plenty (cadences are hours/days).
- Missed wakes (daemon was down at the due time): fire once on next tick (catch-up),
  don't stack.

## Acceptance

- `dejima wake foo --every 720h` on a hibernated island → island wakes ~monthly
  without any host timer or resident process, across daemon restarts.
- Schedule is visible (`dejima schedule list foo`) and cancelable.
- Each scheduled wake appears in the audit/event stream.

## Consumer

The drift-checker watchtower (tools/drift-checker/): today it self-re-arms a short
harness wakeup and stays resident. With this primitive it hibernates between runs
and the daemon wakes it monthly. See strategy/drift-checker-design.md.
