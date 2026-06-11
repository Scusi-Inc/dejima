# Dejima — Multiple agents per island (design & decision record)

**Status:** Draft — approved direction, pre-implementation
**Last updated:** 2026-06-11
**Supersedes:** the v1 deferral "Concurrent agents inside a single island" (`v1-spec.md` §5 Out), and the v1.x open question "Shared workspace volume across islands" (`roadmap.md`).

This document captures the decisions made while planning multi-agent islands. It is the
reviewable record before code lands. It is a living document; phases update it as they ship.

---

## 1. Why

Today an island is a `(repo, agent, name)` tuple — **one container, one agent, one tmux
session** named `dejima`. The 1:1 assumption is baked through every layer (project model,
image entrypoint, API routes, events, presence, TUI, CLI).

The driving use case: *when working on a repo, run several agents together without
permissioning each one separately, sharing context and credentials, with cheap interchange.*
The pain is concrete — re-approving Claude's trust/tool permissions per agent, re-authing
`npm` / `eas` / `gh` / extra repos per island, and the tmux-session sprawl of N sibling
islands that are morally "one project."

This **reverses** the v1 decision to defer concurrent agents. That decision is deliberate to
overturn: it touches the core thesis (containment), so the trade-offs are recorded here in full.

### What this is *not*

Still not an orchestrator / swarm engine (`positioning.md` holds). Dejima provides the
substrate for N co-located agents; deciding *what* they each do, and any cross-agent task
choreography, remains the wrapper's job. The line moves from "one agent per box" to "N agents
per box sharing a containment boundary" — not to "Dejima schedules agents."

---

## 2. The model

```
island "myrepo"  ──►  ONE container (always-alive supervisor)
                        ├─ agent a1  claude-code  tmux: agent-a1   worktree: /workspace
                        ├─ agent a2  claude-code  tmux: agent-a2   worktree: /workspace/.agents/a2
                        └─ agent a3  codex         tmux: agent-a3   worktree: /workspace/.agents/a3
   shared by all agents: /home/dejima (creds + tool auth) · network · /workspace/.git
```

- **One container per island.** Unchanged container boundary; the island stays the unit of
  containment, resource caps, network, and credential mounts.
- **N agents inside it**, each a tmux session (interactive) or a supervised process (headless).
- **Container is a supervisor**, not "the agent process." It boots, ensures the tmux server +
  worktrees + initial agent sessions, then stays alive. Agents are added/removed at runtime via
  `docker exec` without recreating the container.

### `Project` / `AgentSpec`

```
Project (island)                         AgentSpec
  Name        string                       ID         string   // "a1" — stable; used in routes/tmux/worktree/logs
  RepoURL     string                       Type       string   // handler id: claude-code | codex | headless | …
  Image       string                       Label      string   // user-facing, e.g. "frontend"
  Resources   …                            Cmd        string   // headless only
  DesiredState…                            Branch     string   // git branch backing its worktree
  Agents    []AgentSpec   ◄── new          Worktree   string   // /workspace or /workspace/.agents/<id>
                                           Restart    bool      // supervise + restart on crash (headless)
                                           CreatedAt  time.Time
```

`ID` is the durable handle everywhere: route segment, tmux session `agent-<id>`, worktree dir,
log file, env var `DEJIMA_AGENT_ID`. `Label` is cosmetic and renamable.

---

## 3. Handler registry (the abstraction decided up front)

"Agent type" today is a stringly-typed `case` in `image/start.sh` + a hardcoded
`credentialBindMounts()` in Go + an optional `image/agents/<name>/` shim. Adding a first-class
agent is a Go change + image rebuild. Since `Agent string` is becoming `[]AgentSpec` anyway,
this is the moment to promote that into a **declarative handler descriptor** so new handlers
(openclaw, Hermes, Claude/Codex SDK loops, …) are config, not code.

```
Handler
  id            // "claude-code", "codex", "claude-sdk", "openclaw", "hermes"
  kind          // interactive (tmux)  |  headless (supervised process)
  launch        // command + args  (e.g. "claude" / "codex --sandbox-policy=no-sandbox")
  attachable    // derived from kind
  stateDirs     // home subpaths to persist (~/.claude, ~/.codex, ~/.agent-state, …)
  credentials   // host mounts to copy/seed in (today: hardcoded credentialBindMounts)
  eventHook     // how it emits agent.* (Claude hooks / Codex notify / generic curl-to-socket)
  template      // workspace context file (CLAUDE.md / AGENTS.md), optional
```

This is the union of what `agent-adapters.md` already describes informally. claude-code, codex,
and headless are the first three implementations. Anything richer than the registry —
*managing containers Dejima didn't provision* — is deferred (see §11).

---

## 4. Agent shapes, supervision, logs

Two shapes, both first-class in the agent list:

- **Interactive** (claude-code, codex): owns a tmux session `agent-<id>`; attachable; multi-device
  shared screen via tmux's native multi-attach (unchanged mechanism, just per-agent now).
- **Headless** (SDK loops, workers): a **supervised background process**, started detached via
  `docker exec`. Not attachable — the session route returns 409 *for that agent id*, not for the
  whole island (an improvement over today's whole-island 409).

Because the container no longer *is* the headless process, two things headless previously lacked
become necessary and are delivered here:

- **Per-agent log capture.** `docker logs` only sees PID 1 (now the supervisor). Each headless
  agent's stdout/stderr is redirected to `/home/dejima/.dejima/agents/<id>.log`; `dejima logs
  <island> --agent <id>` tails it. (Interactive agents remain captured by tmux.)
- **Restart-on-crash.** Headless agents can die independently of the container, so a lightweight
  per-agent supervisor with a restart policy (`AgentSpec.Restart`) is required — this finally
  ships the long-deferred "supervisor mode" as a side effect of multi-agent.

---

## 5. Git worktrees

All agents share `/workspace/.git`. Working-tree layout:

- **Primary agent** uses `/workspace` (the integration checkout) — keeps migration trivial and
  "where's my code" intuitive.
- **Additional agents** each get `git worktree add /workspace/.agents/<id> -b agent/<id>`. No file
  races; agents integrate via normal git.

Decided trade-off: the asymmetry (primary on `/workspace`, others on worktrees) over "every agent
on its own worktree with `/workspace` as a bare integration point." Asymmetry wins on migration
and ergonomics; can normalize later if it grates.

On agent removal: prune the worktree dir, **leave the branch** (work is precious).

---

## 6. Shared home & "collective permissioning"

The credential/permission win is mostly *free* in the one-container model: all agents share
`/home/dejima`, so Claude trust + tool-permission allowlists, `~/.npmrc`, `~/.config/gh`, `eas`
tokens, extra-repo git credentials — authed once by any agent — are visible to all.

The work is **persistence + scope**: promote today's per-type agent-state volume into a per-island
**`/home/dejima` persistent volume** (whole-home, decided over a curated allowlist — simplest, and
it makes arbitrary tools like `eas` "just work" without chasing each tool's token path). It
survives hibernate/wake and container recreate.

**Risk to watch (early dogfood):** two concurrent Claude processes share `~/.claude`. Settings /
permission allowlist / credentials are read-mostly (exactly the win), but session-history / todo
state may interleave. MVP shares it (that's the point); if it bites, split only *runtime* state
per agent while keeping shared settings/creds.

---

## 7. Addressing & API

New sub-routes under the island; old routes aliased to the **primary agent** for back-compat.

```
GET    /v1/islands/{name}/agents                 list agents
POST   /v1/islands/{name}/agents                 add an agent (launch session/process + worktree)
GET    /v1/islands/{name}/agents/{id}            agent detail
DELETE /v1/islands/{name}/agents/{id}            remove (kill session/proc; prune worktree, keep branch)
GET    /v1/islands/{name}/agents/{id}/session    attach (ws)  — 409 if headless
GET    /v1/islands/{name}/agents/{id}/logs       per-agent logs

GET    /v1/islands/{name}/session   → alias → primary agent (existing clients keep working)
```

- `IslandInfo` gains `Agents []AgentInfo` (`{ID, Type, Label, State, Branch, Worktree, Attachable,
  AgentState, Attached, Stats?}`). For existing islands it carries exactly one entry.
- `CreateIslandRequest` accepts `Agents []AgentSpec` (defaults to one — fully back-compat).
- File verbs (`files`, `cp`, `exec`) stay island-scoped, **defaulting to the primary worktree
  (`/workspace`)** — no new surface in the MVP. Per-agent targeting is roadmapped.

---

## 8. Events, presence, agent-state

- `events.Event` gains optional `Agent string`. `client.*` and `agent.*` carry the agent id.
- `agentStates` and the per-island event ring become keyed by `(island, agent)`.
- The codex/claude turn-complete hook reads `DEJIMA_AGENT_ID` (injected into each session's env)
  and includes it in the POST to `/v1/internal/agent-event`.
- Presence trackers and `MaxClientSize` key by `(island, agent)`. Two clients on agent A share a
  screen; a client on agent B is independent.

---

## 9. TUI

Flat island list → **two-level tree** (island → agents).

- `tuiModel`: replace `selected int` with a flattened visible-row model (island-header rows +
  indented agent rows) with expand/collapse — simplest for bubbletea nav.
- `renderList`: island row (caret, name, agent count) → agent rows (glyph, label/type, state,
  agent-state `!`). `renderDetail`: agent-selected shows branch/worktree/state/attached/events;
  island-selected shows repo/container/resources + agent summary.
- open-in-new-window (`tui_window.go`) carries the selector: `dejima connect <island> --agent <id>`.
- Add-agent action from the dashboard (e.g. `a`); multi-agent option in the create flow
  (`tui_create.go`, currently a single-pick over `knownAgents`).

---

## 10. CLI

- `dejima connect|logs <island> [--agent <id>]`, plus `<island>/<id>` shorthand.
- `dejima agent add|rm|ls <island>` for lifecycle.
- `dejima init --agent claude-code --agent codex …` to seed an island with several agents.
- All existing single-agent invocations keep working (resolve to the primary agent).

---

## 11. Deferred (recorded on `roadmap.md`)

- **Manage foreign containers** Dejima didn't provision (Portainer/compose territory). Parked;
  the committed direction is the open-ended *handler registry* instead.
- **Per-island nested containers (rootless DinD)** — agents spawning their *own* containers
  (test sandboxes, image builds). Dejima keeps no visibility into these by design. Today islands
  have no Docker access; if enabled, **only** the rootless-DinD door (mounting the host docker
  socket is host-root — a containment break, rejected). Parked.
- **Agent-scoped / richer file access** — `--agent` targeting on `cp`/`exec`/`files`, browse API.
  MVP defaults to the primary worktree.
- **Local content ingestion** for content-digesting agents (e.g. OpenClaw on emails/docs) —
  leaning "wrapper feeds it in over the API," but open.
- **Per-agent resource caps** — caps are per-container today; N agents now share them. Per-agent
  cgroups-in-container is out of scope; noted.

---

## 12. Migration & back-compat

- Read legacy scalar `Agent`/`Cmd` and synthesize a single `a1` agent on load; re-persist on next
  write. Existing islands keep working with no user action.
- Old API routes and CLI invocations resolve to the primary agent.
- Headless islands: stay single-agent for the MVP (a container can't be both "headless PID 1" and
  "tmux host"; in the new supervisor model headless becomes a process — single-agent headless
  islands migrate cleanly, multi-agent-with-headless is additive).

---

## 13. Phased plan

| Phase | Goal | Ship/test gate |
|------|------|----------------|
| **0** | `Agent string` → `[]AgentSpec` + migration | existing islands load; `ls`/`status` unchanged; TOML round-trips |
| **0.5** | Handler descriptor; claude-code/codex/headless as descriptors | type system shaped for new handlers; no behavior change |
| **1** | Container-as-supervisor: dynamic tmux sessions, worktrees, headless-as-process, per-agent logs + restart, shared `/home/dejima` volume | add a 2nd session to a live island, attach; `npm`/`gh` login shared; headless restarts on crash |
| **2** | API: agents addressable; old routes aliased | `curl` add a 2nd agent and attach to it |
| **3** | Attach/presence per agent | two clients share agent A; agent B independent; **validate Windows-client → macOS-daemon resize** |
| **4** | Events + agent-state keyed by `(island, agent)` | two agents emit `agent.task-complete` independently; `status` shows each |
| **5** | TUI two-level tree | create 2-agent island from TUI, navigate, open each in its own window |
| **6** | CLI verbs + doc reconciliation | full happy path; old single-agent commands still work; `v1-spec.md` §5 updated |

---

## 14. Open questions / risks

- Shared `~/.claude` under concurrency (see §6) — verify early.
- Headless-as-supervised-process restart semantics (backoff? max retries? crash-loop guard).
- Worktree creation cost/latency on agent add for large repos.
- Whether the primary-agent worktree asymmetry (§5) should be normalized before GA.
- Resource-cap fairness across co-located agents (§11) — observe during dogfood.
