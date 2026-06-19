# Agent lanes

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

**Isolation (no worktree conflicts):** run **one island per lane** — a separate
`dejima init` of this repo per agent, so each has its own `/workspace` checkout.
Then worktrees can't overlap by construction; lanes only ever meet at *merge* time
on a shared file, which the per-lane branch + "commit your own hunks, rebase" rule
handles. Do **not** put two lane-agents in the same island/worktree.

**Start order:** Lanes 3 & 4 have no dependencies — start them immediately.
Lanes 1 & 2 coordinate two things early (Lane 1's ledger append-interface; Lane 2's
identity-on-request), and the **activity feed** is the last item, gated on both.
Each lane works on its own branch and PRs to `master`; if a shared file conflicts,
commit only your own hunks and rebase.
