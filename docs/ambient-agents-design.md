# Ambient / monitoring agents — design

**Status:** designed 2026-06-22; roadmapped as **post-core, NOT launch-blocking**. A distinct
class from Dejima's core (coding agents in islands): **ambient, scheduled, long-running
monitor/assistant agents** — repo watch, email/feedback triage, competition tracking,
news/industry digests. Also dogfooding: the maintainer's own monitors run *on* Dejima, with
containment + audited brokered access to real accounts.

## Principles

- **Separate identity from the test account.** `dejimaqa` is throwaway/test (sandboxed,
  spend-capped). Monitors do real work with real data → run under the owner's real setup (or a
  dedicated "assistant" identity), never the test account.
- **Match the agent type to the job** — not Claude Code for everything (it's a *coding* agent).
- **Brokered, audited access** to real data (Gmail/web/repo) via MCP/Port/capability — the
  governance win: a monitor touching your inbox is an explicit, logged grant, not ambient access.
- **Containment stays default** — each monitor is an island, deny-all, with explicit grants only
  for its source.

## Agent-type mapping

| Monitor | Best-fit agent | Why |
|---|---|---|
| Repo (issues / PRs / CI / commits) | Claude Code, or a webhook-driven light agent | code-aware; can reason about diffs/PRs |
| Email / feedback | Hermes (messaging bridge), or an assistant + **Gmail MCP** | messaging/routing is its lane |
| Competition / news / industry | **Letta** | persistent memory — it *remembers* what it's seen, which trend-monitoring needs |
| Coordinator / front | **OpenClaw** Home Island | idles, gateway, aggregates the others into one digest |

## The scheduling gap (the real new primitive)

Dejima's triggers today are **interactive** (attach) and **event** (webhooks, wake-on-message).
Monitoring needs **time-based** triggers (poll every N / cron) — which don't exist yet. Proposal:
a **scheduler that cron-wakes an island and runs an agent task**, reusing the wake-on-message
machinery (it's the *time-driven* twin of the *event-driven* wake). Combined with
idle-auto-hibernate, this gives **cheap ambient agents**: hibernate between runs, wake on
schedule, do the task, notify, hibernate again. This scheduler is the enabling feature for the
whole class.

## Brokered-creds model

- Gmail / Calendar / Drive via **MCP** (deny-all, per-island grant).
- Web / news via a granted fetch/search MCP tool.
- Repo via the GitHub identity + webhooks.
- Every access **ledgered** — governed + auditable, not an agent with free rein over your inbox.

## Output / actions

- **Default = read → summarize → notify** (a digest to email / the owner). Low-risk; no gate needed.
- **Taking actions** (reply to an email, file an issue, post) routes through the **action-tier gate**
  — deny-all + per-action approval + audit — *reusing Lane 5's action-delegation gate*. So a
  monitor can't auto-reply or act without an explicit, logged approval. Clean reuse.

## Phased rollout

- **P1 — prove the pattern.** One assistant island (OpenClaw Home, or Letta for the memory-heavy
  monitors) + brokered Gmail + web MCP + a manual/poll trigger → a digest. No new primitives.
- **P2 — the scheduler.** Cron-wake islands (the enabling primitive); hibernate-between-runs.
- **P3 — per-domain monitors.** Repo / email / competition / news as separate scheduled islands; a
  coordinator (Home Island) aggregates into one digest.
- **P4 — gated actions.** Let a monitor take actions (reply/triage/file) through the approval gate.

## Explicitly not launch-blocking

Post-core. Does **not** block the solo-dev launch or the public beta. It's a dogfooding +
roadmap-shaping track that validates three things the product may want anyway: **scheduled/ambient
agents**, the **brokered-creds-to-real-accounts** model, and **assistant-agent ergonomics** beyond
coding. Pick it up after the core launch lands.
