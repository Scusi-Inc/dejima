# Agent lanes

## Wave 0.6

| Lane | Brief | Prompt to give the agent |
|---|---|---|
| Completeness (adapters · Port read-norm · macOS Shortcuts · observability) | `docs/lanes/0.6-completeness.md` | "Read `docs/lanes/0.6-completeness.md` and follow it — it's your full brief." |
| 5 — Inter-island exchange (design: `docs/inter-island-exchange-spec.md`) | `docs/lanes/lane-5-inter-island.md` | "Read `docs/lanes/lane-5-inter-island.md` and follow it — it's your full brief." |
| 5 (Phase 3.5) — wake-on-message + recipient contract | `docs/lanes/lane-5-phase-3.5-wake.md` | "Read `docs/lanes/lane-5-phase-3.5-wake.md` and follow it — it's your full brief." |
| 6 — Automated test harness (Phase A; design: `docs/testing/automated-test-harness.md`) | `docs/lanes/lane-6-test-harness.md` | "Read `docs/lanes/lane-6-test-harness.md` and follow it — it's your full brief." |

**Lane 5 status:** Phases 1–3 shipped (mailbox #24, info link #25, action gate #27);
Phase 3.5 (wake + recipient contract) is the active piece; Phase 4 (A2A discovery)
is optional. The whole inter-island wave awaits Minion live-verify before a `v0.6.0` tag.

## Wave 0.5 (shipped)

| Lane | Brief | Prompt to give the agent |
|---|---|---|
| Onboarding (Mac-mini wizard) | `docs/lanes/0.5-onboarding.md` | "Read `docs/lanes/0.5-onboarding.md` and follow it — it's your full brief." |
| SDK publish (PyPI/npm) | `docs/lanes/0.5-sdk-publish.md` | "Read `docs/lanes/0.5-sdk-publish.md` and follow it — it's your full brief." |
| Hardening (keychain + idle-hibernate) | `docs/lanes/0.5-hardening.md` | "Read `docs/lanes/0.5-hardening.md` and follow it — it's your full brief." |

The 0.5 **terminal auto-reconnect** fix already shipped (PR #14) — it just needs
Minion live-verify (drop the link mid-session → terminal reconnects, doesn't close).
All three lanes above are independent; start them in parallel. Wave-0.4 lane briefs
(audit / team-auth / mcp / sdk) are below for reference.

---

# Wave 0.4 lanes (shipped — reference)

Parallel work briefs so up to four agents (each in its own island/worktree) can
build the committed queue without colliding. See `docs/roadmap.md` →
"Committed build queue" and "Parallel lanes" for the plan, gates, and shared seams.

How to start each agent: spin it up in its own island on a feature branch, then
give it the one line below. The brief carries everything else.

| Lane | Brief | Prompt to give the agent |
|---|---|---|
| 1 — Audit core | `docs/lanes/lane-1-audit.md` | "Read `docs/lanes/lane-1-audit.md` and follow it — it's your full brief." |
| 2 — Team auth & roles | `docs/lanes/lane-2-team-auth.md` | "Read `docs/lanes/lane-2-team-auth.md` and follow it — it's your full brief." |
| 3 — MCP brokering | `docs/lanes/lane-3-mcp.md` | "Read `docs/lanes/lane-3-mcp.md` and follow it — it's your full brief." |
| 4 — SDK & clients | `docs/lanes/lane-4-sdk.md` | "Read `docs/lanes/lane-4-sdk.md` and follow it — it's your full brief." |

**Isolation (no worktree conflicts):** one island is plenty — add **one agent per
lane** (`dejima agent add`), and Dejima puts each agent on its **own git worktree**
(`/workspace/.agents/<id>`) on its **own branch**, all sharing one `.git` (worktrees
share the object store — not repo copies). Files can't collide because each agent
works in a different worktree; lanes meet only at *merge* time, handled by the
per-lane branch + "commit your own hunks, rebase" rule. **The one hard rule:** each
agent stays in its **own worktree** — never two agents in the same worktree, and
don't work in `/workspace` itself (that's the primary/master worktree; committing
there lands on `master`).

**Start order:** Lanes 3 & 4 have no dependencies — start them immediately.
Lanes 1 & 2 coordinate two things early (Lane 1's ledger append-interface; Lane 2's
identity-on-request), and the **activity feed** is the last item, gated on both.
Each lane works on its own branch and PRs to `master`; if a shared file conflicts,
commit only your own hunks and rebase.
