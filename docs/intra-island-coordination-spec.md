# Spec: intra-island agent coordination (discovery + notify)

> Status: proposal · Audience: backend agent · Author: agent d6 (2026-06-24)
>
> Motivating incident: an agent (`d6`) needed to hand a task off to a peer agent
> ("the TUI agent", id `a2`) **in the same island**. The mailbox worked, but the
> agent had **no way to discover who its peers are** — it couldn't list the
> island's agents, couldn't map the human-facing label ("TUI") to an id (`a2`),
> and could only address `a2` because a human told it the id out-of-band. This
> spec proposes the minimal, containment-preserving changes to make legitimate
> *intra-island* coordination ergonomic.

## Containment invariant (do not break)

Island tokens are **island-scoped**: `internal/porttoken/porttoken.go:64`
(`IslandForToken`) resolves a bearer token to exactly one island, and
`internal/api/tokenauth.go:162` (`authorizeToken`) default-denies every route not
in the `tokenRouteAccess` allowlist (`tokenauth.go:63`). An island token must
**never** be able to read or enumerate *other* islands. Every change below is
**own-island, read-only**, scoped by the existing `accessOwnIsland` mechanism
(`tokenauth.go:174`, which checks the route's `{name}` equals the token's island).
Nothing here grants cross-island visibility; the fleet-wide surfaces
(`dejima ls`, `dejima agent ls <other>`, `dejima activity`) stay operator-only.

## What already exists (don't rebuild)

- **Mailbox** — `dejima msg send/poll`, daemon at `internal/api/mailbox.go`, store
  `internal/mailbox/mailbox.go` (in-memory ring, 256 msgs/island). Already
  allowlisted as `accessOwnIsland`. Model `mailbox.Message{Seq,Island,From,To,
  Topic,Payload,Time}` (`mailbox.go:17`). Per-island cursor via `Latest()`
  (`mailbox.go:177`) + `--since`.
- **Notify-on-message** — `internal/api/wake.go`. `SetArrivalHook`
  (`mailbox.go:78`) is wired at `server.go:238`; `onMailboxArrival` (`wake.go:74`)
  emits a `mailbox.arrival` event and injects a batched, idle-boundary nudge
  ("📬 N new message(s) — run: `dejima msg poll`") into the recipient's tmux
  (`wake.go:114,140`). **This means terminal agents are already notified.** The
  remaining gaps are below (P2), not a from-scratch build.
- **Agent labels** — `AgentSpec.Label` (`internal/project/project.go:58`) already
  stores the renamable display name ("TUI"), persisted in
  `~/.dejima/projects/<name>/dejima.toml`, returned in `AgentInfo.Label`
  (`internal/api/types.go:47`). The data the discovery gap needs **already exists**
  on the daemon — it's just not reachable by an island token.

---

## P1 — Self-island agent roster for island tokens  ★ highest value, lowest risk

Let an island token call `GET /v1/islands/{name}/agents` **only when `{name}` is
its own island**, returning a **reduced** `AgentInfo` so an agent can resolve
peers (id ↔ label ↔ type ↔ state).

### Change
- Allowlist the route as own-island only — `internal/api/tokenauth.go:63`
  (`tokenRouteAccess` map), add:
  ```go
  "GET /v1/islands/{name}/agents": accessOwnIsland,
  ```
  No change to `authorizeToken` — the `accessOwnIsland` branch (`tokenauth.go:174`)
  already enforces `{name}` == token's island.
- Handler `s.listAgents` (`internal/api/server.go:832`) currently returns the full
  `AgentInfo`. For island-token callers, return a **projection** that drops
  operational/credential-adjacent fields. Detect the caller via
  `TokenIslandFromContext()` (`tokenauth.go:106`) being set (vs operator/identity
  path) and serialize a reduced view.

### Reduced projection (island-token view)
Keep: `ID`, `Label`, `Type`, `State`, `Worktree`, `Branch`.
Drop: `Provider`, `Model`, `Token`/auth, `Restarts`, `Error`, `Tmux`,
`Attached`, `CreatedAt` (anything that is config, credential, or attach-surface).
Rationale: peers share `/workspace` and the same home/credentials already (same
trust silo per `tokenauth`/`mailbox` design), so id/label/type/state/branch is no
new information leak *within* the island — but provider/model/token are config
that contained agents shouldn't enumerate.

### Acceptance
- From inside an island: `dejima agent ls $DEJIMA_PROJECT_NAME` succeeds and lists
  peers with their labels; e.g. a row `a2  TUI  claude-code  running`.
- `dejima agent ls <some-other-island>` from an island token still returns
  `route not permitted` / not-own-island (verify the `{name}` mismatch path).
- Operator/team tokens are unchanged (still get the full `AgentInfo`).

### Security review notes (flag for sign-off)
The original threat model omitted `agent ls` from island tokens deliberately
(autonomy limits). This proposal argues same-island peer metadata is already
within the agent's trust boundary, but **get explicit sign-off** that exposing
labels/branches/worktrees of co-resident agents is acceptable. If even that is too
much, fall back to P1-lite below.

### P1-lite (if full roster is rejected)
Add a dedicated, minimal endpoint `GET /v1/islands/{name}/peers` returning only
`[{id, label, type, state}]` for own-island — strictly a directory, no branch /
worktree / config. Same allowlist mechanism. CLI: `dejima msg peers` (lives under
`msg` since it's the addressing directory for messaging).

---

## P2 — Close the notify gaps (mostly polish; core already shipped)

`wake.go` already nudges terminal agents. Remaining gaps:

1. **Headless agents aren't nudged** — the tmux inject is a no-op for headless
   (`wake.go`, no PTY). They rely on polling. Acceptable for now; document it. If
   needed later, surface the `mailbox.arrival` event to headless agents via their
   event stream so an SDK loop can react.
2. **Nudge lacks context** — it says "N new message(s)" but not from whom / topic.
   Consider including `from`/`topic` of the most recent message in the nudge text
   (`wake.go:114`) so the recipient can triage without polling. Keep it one line.
3. **No send-side delivery signal** — `dejima msg send` returns `sent #N → a2`
   with no indication whether the recipient is reachable/attached. Optional:
   have `send` report the recipient's current `State` (running/stopped) so the
   sender knows if a nudge will land or if the peer is dormant. (Depends on P1's
   roster data.)

P2 is **lower priority** than P1 — the core notification path works. Treat (2) as
a nice small win, (1) and (3) as backlog.

---

## P3 — Scoped own-island activity read  (medium value, more work)

Let an island token call `GET /v1/activity` filtered to **its own island only**, so
an agent can see recent broker/lifecycle events for its island ("a2 last traded
file X", "grant added") before pinging a peer.

### Change
- Allowlist `internal/api/tokenauth.go:63`:
  ```go
  "GET /v1/activity": accessTokenOwn,
  ```
  (`accessTokenOwn` = any valid token, handler self-authorizes — `tokenauth.go`.)
- Handler `s.handleActivity` (`internal/api/activity.go:83`) currently scopes only
  via `IdentityFromContext()` (operator/roleauth path, `activity.go:106`), which is
  empty for island-token callers. Add a token-path branch: read
  `TokenIslandFromContext(r.Context())`; if set, **force** the island filter to
  that island and drop all account/system (no-island) items. This must be a hard
  pin, not a user-supplied `?island=` filter, so an island token cannot widen it.

### Acceptance
- Island token: `dejima activity` (add an island-token code path / or new
  `dejima status --activity`) returns only that island's items; never another
  island's, never account-level entries.
- Operator tokens: behavior unchanged.

### Note
Activity is an O(entries) scan over `~/.dejima/ledger.jsonl` with no durable
island index (`activity.go`). Fine at current scale; if island-token polling makes
this hot, add an island index. Lowest priority of the three.

---

## Suggested sequencing

1. **P1** (or P1-lite) — unblocks the actual reported pain (peer discovery /
   name→id). One allowlist line + a reduced projection + security sign-off.
2. **P2.2** (context in nudge) — tiny, high quality-of-life.
3. **P3** — only if agents start needing situational awareness beyond the roster.

## Non-goals
- No cross-island visibility for island tokens (ever).
- No agent-level auth boundary — agent id remains self-reported within an island's
  shared trust silo (`tokenauth`/`mailbox` already document this); these reads are
  island-scoped, not agent-scoped.
- No new persistence layer for the mailbox (ephemeral ring is fine for now).

## Open questions for the backend agent / security
- Is same-island peer metadata (label/branch/worktree) acceptable to expose to a
  contained agent, or do we ship P1-lite (id/label/type/state only)?
- Should the peer directory live on `agent ls` (reuse) or a new `msg peers`
  (clearer that it's the messaging address book)?
- For P2.2, is leaking `from`/`topic` into the tmux nudge acceptable, or does that
  cross-contaminate sessions in a way we want to avoid?
