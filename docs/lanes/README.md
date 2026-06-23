# Agent lanes

## Launch wave (Show HN gate — full plan: `strategy/launch-tasklist.md`)

Gate sequence: **0 → {A ∥ B ∥ C} → launch.** Lanes A, B, C are independent — start
in parallel. Lane 0 is the human-owned virgin-Mac test substrate that proves A and B.

| Lane | Brief | Prompt to give the agent |
|---|---|---|
| 0 — Virgin-Mac test substrate (**human-owned**, not an agent) | `docs/lanes/lane-0-test-substrate.md` | n/a — run the proof procedures the A/B PRs ship |
| A — Bulletproof install + first-run (P0.1, the gate) | `docs/lanes/lane-a-install.md` | "Read `docs/lanes/lane-a-install.md` and follow it — it's your full brief." |
| B — Uninstall that doesn't nuke agents (P0.2) | `docs/lanes/lane-b-uninstall.md` | "Read `docs/lanes/lane-b-uninstall.md` and follow it — it's your full brief." |
| C — Grants API: unified per-island grants view (P0.3) | `docs/lanes/lane-c-grants-api.md` | "Read `docs/lanes/lane-c-grants-api.md` and follow it — it's your full brief." |

**Note on Lane C:** the per-resource grant endpoints (port/capability/mcp/links) already
exist over the daemon API — the "MCP is CLI-only" premise is stale. Lane C is the thinner
unified-view + OpenAPI-parity + deny-all-verification job. See the brief's "Reality check".

---

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
