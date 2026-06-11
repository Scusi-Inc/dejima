# Multi-agent islands — implementation plan

**Status:** Draft — engineering breakdown for `multi-agent-spec.md`
**Last updated:** 2026-06-11
**Companion to:** [`docs/multi-agent-spec.md`](multi-agent-spec.md)

File/function-level plan to take Dejima from one-agent-per-island to N. Grounded in the
current code (function names + line refs are as of `85eeaf4`). Each phase is independently
shippable and leaves `master` green.

---

## A. Implementation decisions (defaults — veto any)

These are *how*, not *what*; the design is locked in the spec.

1. **Daemon-driven reconciliation, not container self-launch.** The container becomes a bare
   supervisor: `image/start.sh` still does first-boot bootstrap (ownership fix, git/gh creds,
   shim, clone) but **no longer launches the agent**. The daemon owns agent sessions and
   creates/restores them via `rt.Exec` from a single source of truth (`Project.Agents`).
   `reconcileAgents(ctx, p)` is called after create, after `wake`, and from `AdoptExisting`.
   Rationale: add/remove/restart/wake all go through one path; no manifest-in-volume drift.

2. **Supervisor mode is opt-in per island via env, so headless doesn't regress.** start.sh
   branches on `DEJIMA_SUPERVISOR`: interactive/multi islands set it (`=1`) → "ensure tmux
   server + tail forever, daemon execs sessions in." A **pure single-agent headless** island
   leaves it unset → today's `exec "$AGENT_CMD"` as PID 1, `docker logs` unchanged. Headless
   *coexisting with interactive agents* (supervised process + per-agent logfile + restart) is
   **deferred to Phase 7** so the interactive MVP stays small and headless behavior is preserved.

3. **tmux session name is stored on the agent, not derived blindly.** `AgentSpec.Tmux`:
   migrated primary gets `"dejima"` (so a live, attached session survives the daemon upgrade —
   reconcile finds it already running and adopts it); new agents get `"agent-<id>"`. Replaces
   `const tmuxSession = "dejima"` in `session.go:21`.

4. **Per-agent git worktrees via daemon exec.** Adding a non-primary agent runs
   `git -C /workspace worktree add /workspace/.agents/<id> -b agent/<id>` through `rt.Exec`
   (guarded on `/workspace/.git` existing). Primary stays on `/workspace`. Removal prunes the
   worktree dir, keeps the branch.

5. **Agent IDs are sequential `a<N>`, daemon-allocated, monotonic per island.** Next id =
   `max(existing N)+1`; never reused within an island's life. Durable handle for route segment,
   tmux name, worktree dir, log path, and the `DEJIMA_AGENT_ID` env injected into each session.

6. **Old routes/CLI alias to the primary agent.** Primary = `Agents[0]` (the migrated/first
   agent). `…/islands/{name}/session`, `dejima connect <island>`, `cp`/`exec`/`logs` all keep
   working unchanged; file verbs default to the primary worktree (`/workspace`).

7. **Bump `APIVersion`** (in `OverviewResponse`) so clients can detect multi-agent-capable
   daemons; gracefully degrade in the TUI when talking to an older daemon (single-entry list).

---

## B. Naming / ID conventions

| Thing | Pattern | Source |
|---|---|---|
| Agent id | `a1`, `a2`, … | daemon-allocated |
| tmux session | `dejima` (migrated primary) / `agent-<id>` | `AgentSpec.Tmux` |
| Worktree | `/workspace` (primary) / `/workspace/.agents/<id>` | `AgentSpec.Worktree` |
| Branch | repo default (primary) / `agent/<id>` | `AgentSpec.Branch` |
| Headless logfile | `/home/dejima/.dejima/agents/<id>.log` | Phase 7 |
| Env in session | `DEJIMA_AGENT_ID=<id>` | injected on `tmux new-session` |

---

## Phase 0 — data model + migration (no behavior change)

**`internal/project/project.go`**
- Add `AgentSpec` struct: `ID, Type, Label, Cmd, Tmux, Branch, Worktree string`, `Restart bool`,
  `CreatedAt time.Time` (toml tags). Add `Agents []AgentSpec` to `Project` (toml `agent,...`).
- Keep legacy scalar `Agent`/`Cmd` fields for read. In `Load` (line 117), after unmarshal, if
  `len(p.Agents)==0 && p.Agent!=""`, synthesize `Agents=[]AgentSpec{{ID:"a1", Type:p.Agent,
  Cmd:p.Cmd, Tmux:"dejima", Worktree:"/workspace"}}`. Leave legacy fields populated (don't break
  older daemons reading the same file) — `Save` writes both until a later cleanup phase.
- Helpers: `(p *Project) PrimaryAgent() *AgentSpec`, `AgentByID(id) (*AgentSpec, bool)`,
  `NextAgentID() string`, `AddAgent(spec)`, `RemoveAgent(id)`. `AgentVolume()` unchanged (still
  one home-state volume per island; §Phase 1 repurposes its mount target).

**`internal/api/types.go`**
- Add `AgentInfo` struct: `ID, Type, Label, Tmux, Branch, Worktree string`, `Attachable bool`,
  `State string` (session alive?), `AgentState *AgentStateInfo`, `Attached []PresenceEntry`.
- Add `Agents []AgentInfo` to `IslandInfo` (keep scalar `Agent` for back-compat, = primary type).
- `CreateIslandRequest`: add `Agents []AgentSpecRequest` (optional; when empty, fall back to the
  existing scalar `Agent`/`Cmd` → one agent). No field removed.

**Tests:** `project_test.go` — round-trip a legacy single-`agent` TOML and assert one synthesized
`a1`; round-trip a multi-agent TOML; `NextAgentID` monotonicity.

**Gate:** `go test ./...` green; `dejima ls`/`status` byte-identical for existing islands.

---

## Phase 0.5 — handler registry

**New `internal/handlers/handlers.go`**
- `type Handler struct { ID, Kind, Launch string; Attachable bool; StateDirs []string;
  Credentials []CredentialMount; EventHook string; Template string }` (`Kind` ∈
  `interactive|headless`).
- `var registry = map[string]Handler{ "claude-code": …, "codex": …, "headless": … }` encoding
  what's currently spread across `image/start.sh` (the `case`), `agentStateMountTarget`
  (`server.go:850` → `StateDirs`), and `credentialBindMounts` (`server.go:863`).
- `Lookup(type string) (Handler, bool)`, `Attachable(type) bool`, `LaunchCmd(spec) string`.

**Refactors (behavior-preserving):**
- `agentStateMountTarget(agent)` → delegate to `handlers.Lookup(agent).StateDirs[0]`.
- `session.go` headless 409 check → `!handlers.Attachable(p.Agent)`.
- `image/start.sh` `case` stays for now (the image is versioned separately); the registry is the
  daemon's view. A later phase can have the daemon pass `Launch` explicitly so the image `case`
  becomes a fallback.

**Gate:** no behavior change; registry-derived values equal the old hardcoded ones (table test).

---

## Phase 1 — container as multi-agent supervisor

**`image/start.sh`**
- Add the `DEJIMA_SUPERVISOR` branch (decision A.2). When set: do bootstrap, ensure
  `tmux start-server`, **don't** create the `dejima` session here, then `exec tail -f /dev/null`.
  When unset + headless: unchanged. When unset + interactive (older single-agent path during a
  transition): keep creating the `dejima` session so a daemon that hasn't reconciled yet still
  shows an agent. (Daemon sets `DEJIMA_SUPERVISOR=1` for all non-headless islands.)
- Ensure `git worktree` is usable (git already present); pre-create `/workspace/.agents` and
  `/home/dejima/.dejima/agents`.

**`internal/api/server.go`**
- `createContainerForProject` (line 507): set `env["DEJIMA_SUPERVISOR"]="1"` for non-headless;
  repurpose the agent volume mount from `agentStateMountTarget(p.Agent)` to **`/home/dejima`**
  (whole-home persistence, decision in spec §6). Migration note: existing per-type agent volumes
  mounted at `~/.claude` become a `/home/dejima` mount on next container recreate (`reset`/
  `upgrade`); data is volume-scoped so a one-time `upgrade` re-homes it. Document in the phase PR.
- New `reconcileAgents(ctx, p)`: for each `AgentSpec`, ensure worktree exists (exec `git
  worktree add` for non-primary) and ensure the tmux session is running (`tmux has-session -t
  <Tmux>` else `tmux new-session -d -s <Tmux> -c <Worktree> -e DEJIMA_AGENT_ID=<id>
  <handlers.LaunchCmd>`). Idempotent.
- New `addAgentSession(ctx, p, spec)` / `removeAgentSession(ctx, p, id)` (kill session, prune
  worktree). Both serialize under `projectLock(name)`.
- Call `reconcileAgents` from: `createIsland`→`provision`, `wakeIsland` (line 616, after start),
  and `AdoptExisting` (line 252, for running islands) so a daemon restart restores sessions.

**Gate (manual):** on a running island, `POST` is not yet wired — drive `addAgentSession` via a
temporary test hook or unit-level call: second tmux session appears, attaches via
`docker exec -it … tmux attach -t agent-a2`, has its own worktree, shares `~/.npmrc` with a1.

---

## Phase 2 — API: agents addressable

**`internal/api/server.go` `Handler()` (line 219):** add
```
GET    /v1/islands/{name}/agents                 s.listAgents
POST   /v1/islands/{name}/agents                 s.addAgent
GET    /v1/islands/{name}/agents/{id}            s.getAgent
DELETE /v1/islands/{name}/agents/{id}            s.removeAgent
GET    /v1/islands/{name}/agents/{id}/session    s.sessionWS         (reuse; resolve id)
GET    /v1/islands/{name}/agents/{id}/logs       s.handleLogs        (reuse; Phase 7 for headless)
```
- `addAgent`: allocate id, build `AgentSpec` (worktree/branch/tmux per conventions), `p.AddAgent`,
  `p.Save()`, `addAgentSession`, emit `island.agent-added` (new event type), return `AgentInfo`.
- `removeAgent`: `removeAgentSession`, `p.RemoveAgent`, `p.Save()`, emit `island.agent-removed`.
- `toInfo` (line 815): populate `IslandInfo.Agents` by iterating `p.Agents`, querying session
  liveness (`tmux has-session`) and `agentStateOf(name,id)`.

**`internal/api/client.go`:** add `ListAgents`, `AddAgent`, `RemoveAgent`, and overload
`DialSession(ctx, name, agentID, label)` (keep a thin `DialSessionPrimary` for callers that
don't care). Path builds `…/agents/{id}/session` when id set, else legacy `…/session`.

**Gate:** `curl POST …/agents` adds a claude agent to a live island; `DialSession` to it attaches.

---

## Phase 3 — attach + presence per agent

**`internal/api/session.go`**
- Replace `const tmuxSession` usage: `sessionWS` reads optional `{id}` path value → resolve
  `spec := p.AgentByID(id)` (default primary). 409 if `!handlers.Attachable(spec.Type)`.
- Pass `spec.Tmux` to `bridge.MaxClientSize` (line 266) and `bridge.AttachToTmux` (line 271)
  — both already take a `tmuxSession string`, so this is a value swap.
- Presence keyed by `(island, agentID)`: `trackerFor(name)` → `trackerFor(name, id)` (map key
  `name+"\x00"+id`). `client.attached`/`detached` events carry `Agent: id` (Phase 4 adds field).
  `RevokeAllSessions` iterates all trackers (unchanged semantics).

**Gate:** two clients on a2 share a screen; a client on a1 is independent. **Validate the
Windows-client → macOS-daemon resize path** for a non-primary agent (the known-sensitive combo —
see `dejima-windows-resize-fix`).

---

## Phase 4 — events + agent-state per agent

**`internal/events/events.go`:** add `Agent string \`json:"agent,omitempty"\`` to `Event`; add
`TypeIslandAgentAdded`/`TypeIslandAgentRemoved`.

**`internal/api/server.go`:**
- `agentStates map[string]AgentStateInfo` → key by `island+"\x00"+agent` (helper
  `agentStateKey`). `maybeUpdateAgentState` (line 190) reads `e.Agent`; `agentStateOf` →
  `agentStateOf(island, agentID)`.
- `recordEvent`/`IslandEvents` (lines 162/177): event ring stays per-island but each event now
  carries `Agent`; `handleIslandEvents` returns them as-is (clients filter by agent).

**`internal/api/webhooks.go` `handleAgentEvent`:** accept `agent` in `AgentEventRequest`; pass to
`events.Event{Agent: …}`.

**`image/agents/*/hooks/notify.sh`:** include `"agent":"$DEJIMA_AGENT_ID"` in the POST body.

**Gate:** two agents emit `agent.task-complete` independently; `status` shows per-agent state.

---

## Phase 5 — TUI two-level tree

**`cmd/dejima/tui.go`**
- Replace `selected int` with a derived flat row list `rows []treeRow` where
  `treeRow{Kind: islandRow|agentRow, Island string, AgentID string}` plus an `expanded
  map[string]bool`. Cursor moves over visible rows; left/right or `space` collapses/expands.
- `renderList`: island row (caret, name, `N agents`, rollup status) then indented agent rows
  (glyph, label/type, state, `!` agent-state). `renderDetail`: agent row → branch/worktree/
  state/attached/per-agent events; island row → repo/container/resources + agent summary.
- `selectedName()`/open path: carry `(island, agentID)`. `tui_window.go openInNewWindow` →
  `dejima connect <island> --agent <id>`.
- Add `a` = add-agent action (prompt agent type → `client.AddAgent`), `x` = remove-agent
  (guard: can't remove last). Degrade gracefully if `Overview.APIVersion` predates multi-agent
  (render the single synthesized agent; hide add/remove).
- `tui_create.go`: after name step, optional "add more agents?" loop; create with `Agents`.

**Gate:** create a 2-agent island from the TUI, navigate the tree, open each in its own window.

---

## Phase 6 — CLI verbs + doc reconciliation

**`cmd/dejima/main.go`**
- `--agent <id>` (and `<island>/<id>` shorthand) on `connect`, `logs`; resolver helper
  `splitIslandAgent(arg)`.
- New `dejima agent` command group: `add <island> --type claude-code [--label …]`,
  `rm <island> <id>`, `ls <island>`.
- `dejima init` accepts repeated `--agent` to seed multiple agents.
- `dejima ls` gains a tree/`--wide` view listing agents per island.

**Docs:** flip `v1-spec.md` §5 ("Concurrent agents inside a single island" moves Out→In with a
pointer to the spec); update `README.md` ("multi-agent" now literal), `agent-adapters.md`
(handler registry), `positioning.md` (still-not-an-orchestrator note refined).

**Gate:** full happy path on a clean island; every legacy single-agent command still works.

---

## Phase 7 — headless agents as co-located supervised processes (additive)

Only after the interactive MVP is dogfooded.
- start.sh supervisor + daemon `addAgentSession`: for `Kind==headless`, start detached via
  `rt.Exec` with output redirected to `/home/dejima/.dejima/agents/<id>.log`; track pid.
- Per-agent restart policy (`AgentSpec.Restart`): a lightweight supervisor loop (container-side
  helper or daemon watchdog) restarts on exit with backoff + crash-loop guard.
- `handleLogs` honors `--agent <id>` → tail the per-agent logfile; legacy island logs = primary.
- Removes the whole-island headless 409 (already per-agent since Phase 3).

**Gate:** a headless agent runs alongside two interactive agents; crashes and restarts; its logs
tail independently.

---

## C. The 1:1-assumption touch map (audit checklist)

| Assumption | Location | Resolved in |
|---|---|---|
| `Project.Agent` scalar | `project.go:36` | Phase 0 |
| `agentStateMountTarget` per-type | `server.go:559,850` | 0.5 / 1 |
| `const tmuxSession = "dejima"` | `session.go:21,266,271` | 3 (stored on spec, 0/A.3) |
| start.sh launches one agent | `image/start.sh` | 1 |
| Headless whole-island 409 | `session.go:142` | 3 (per-agent) / 7 |
| `agentStates map[string]…` | `server.go:57,190,204,209` | 4 |
| `Event` has no agent field | `events.go:32` | 4 |
| presence per-island | `session.go:116` | 3 |
| routes island-only | `server.go:219-245` | 2 |
| `IslandInfo` no agents | `types.go:6` | 0 |
| `DialSession(name,label)` | `client.go:371` | 2 |
| TUI flat `selected int` | `tui.go` | 5 |
| open-in-new-window no agent | `tui_window.go` | 5 |
| label `dejima.agent` (single) | `server.go:568` | keep (island's primary); per-agent is tmux-scoped |

---

## D. Testing & dogfood

- **Unit:** `project_test.go` (migration, id alloc), handler-registry table test, agent-state
  key test.
- **Integration (host with Docker):** create island → add 2nd agent → attach both → emit events →
  remove agent. Scripted under `scripts/`.
- **Cross-machine:** attach/resize for a non-primary agent on the **Windows client → macOS daemon**
  combo (`minion`/`GIZMO`); this is the historically fragile path.
- **Back-compat:** upgrade a daemon over an existing single-agent island; confirm it loads,
  attaches via the legacy route, and `upgrade` re-homes the agent volume to `/home/dejima`.

## E. Sequencing & safety

- Phases 0→0.5→1 are internal (no API/CLI change) — safe to land incrementally.
- The first **user-visible** capability lands at Phase 2 (API) / Phase 6 (CLI); the TUI tree at 5.
- Each phase keeps legacy routes/commands working; the only data migration is read-time
  (Phase 0) plus a one-time agent-volume re-home on the next `upgrade` (Phase 1), both reversible
  by not upgrading.
- Estimated shape (not a commitment): 0/0.5 ~0.5d each, 1 ~1–2d, 2 ~1d, 3 ~1d, 4 ~0.5d, 5 ~1–2d,
  6 ~1d, 7 ~1–2d.
