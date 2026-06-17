# Launch checklist

A living, per-feature list of what to verify before Dejima ships. The goal is
"every little thing" — each row is a concrete, checkable behavior, not a vague
area. Keep it honest: update the status as coverage changes.

**Legend**
- `[x]` verified / done · `[ ]` not yet
- **A** = covered by an automated test (`go test` / `scripts/integration.sh`)
- **M** = manual verification only (no automated coverage yet)
- **!** = known issue or gap (see notes / task list)

> Cross-references: design specs in `docs/`, security boundary in
> `internal/api/tokenauth.go`, live smoke test in `scripts/integration.sh`.

---

## 0. Launch blockers (must be green before any release)

- [x] **In-island code cannot reach the daemon control plane.** Fixed by
  `feat/secure-island-routing` (merged `57ecb32`): the unix control socket is no
  longer mounted into islands; in-island traffic (incl. `agent-event`) goes only
  over the token-authenticated, island-scoped TCP path. Denial is locked by
  `tokenauth_test.go`. **Live-verify still pending** on a real engine — see
  *Live verification procedures* §L1/§L4. **A** (auth) / **M/!** (live)
- [ ] Fresh-host first run works end to end: install → `dejima init` → connect,
  with no pre-existing image or credentials. **M**
- [ ] The live integration suite passes against a real engine
  (`scripts/integration.sh`). **A**

---

## 1. Install & onboarding

- [ ] Client one-liner install (`install-client.sh`) on macOS + Linux. **M**
- [ ] Server install (`install.sh` / `setup.sh`) provisions a reachable daemon. **M**
- [ ] `dejima onboard` / first-run wizard completes and is re-runnable; dismissal sticks. **M**
- [ ] `dejima` with no args opens the TUI on a real terminal, and prints a hint (not a crash) when stdin/stdout isn't a TTY. **M**
- [ ] `dejima doctor` reports daemon, Docker, image, projects, networks, webhooks honestly. **M**
- [ ] Version skew: client detects daemon/API version mismatch and surfaces it. **M**

## 2. Daemon & listeners (security boundary)

- [ ] Unix socket listener (Linux): operator-only; **no longer mounted into islands** (`secure-island-routing`). **A** (denial) / **M** (bind)
- [ ] Tailnet TCP listener: only tailnet IPs accepted; off-tailnet refused. **A** (tokenauth scope) / **M** (bind)
- [ ] Host-internal token-TCP listener: default-deny, island-scoped; **on by default** (`127.0.0.1:7274`) since it's now the only in-island path. **A** (`tokenauth_test.go`) / **M** (live reach, see §L1)
- [ ] Host terminals listener: `/v1/terminals*` present only with `--host-terminals`; denied to island tokens. **A** (`tokenauth_test.go`, integration) / **M** (live attach, §L3)
- [ ] `dejima service install/uninstall/restart` manages `dejimad` as a host service. **M**
- [ ] Daemon survives restart with all islands/state intact. **M**

## 3. Island lifecycle

- [ ] `init` from a remote URL (clones inside the island). **A** (handler) / **M** (live clone)
- [ ] `init` from a local path — clone-from-origin vs `--local-copy` seed (captures unpushed commits). **A/M**
- [ ] `init` with `--name`, name derivation, and duplicate-name 409. **A**
- [ ] `init --repo "" --seed_path` (no-remote local copy) succeeds; no repo + no seed → 400. **A**
- [ ] `ls` / `status` show accurate state, container status, presence. **M**
- [ ] `hibernate` → `wake` preserves workspace + home volumes. **M**
- [ ] `reset` clears agent state, preserves workspace, re-seeds credentials. **M**
- [ ] `upgrade` recreates container(s) on the current image, all state kept. **M**
- [ ] `purge` / `DELETE` removes container, both volumes, network, project dir, **and** per-island secrets (gh token). **A** (gh-token cleanup) / **M** (volumes)

## 4. Multi-agent

- [ ] Seed multiple agents at create (`--agent` repeated / `agents[]`). **A**
- [ ] Add agent to a running island; primary cannot be removed (409). **A**
- [ ] Per-agent git worktree + branch + tmux session for interactive agents. **A**
- [ ] Shell agent works on `/workspace` directly (no isolated worktree). **A**
- [ ] Headless agent: requires a cmd, runs supervised with restart + per-agent log. **A**
- [ ] OpenClaw agent: baked launch, self-installs, non-attachable. **A**
- [ ] Relabel / remove agent; logs route to the right per-agent file. **A**

## 5. Agent identity & display

- [ ] New island primary id uses the island letter (`Port`→`p1`); added agents continue (`p2`,`p3`). **A**
- [ ] Legacy islands keep `a1` (EnsureAgents back-fill, no live-session rename). **A**
- [x] `SetPrimaryID` unit-covered (rename + tmux derive, no-op cases). **A** (task #7)
- [ ] List rows lead with the name (label, or id when unlabeled); detail view shows the id handle. **A**
- [x] Duplicate labels don't render two identical rows (id handle appended). **A** (task #10)

## 6. Sessions & multi-device

- [ ] `connect` attaches; multiple clients share one screen (multi-attach + presence). **M**
- [ ] Session survives client disconnect and reconnect from a different device. **M**
- [ ] `logout-all` / `sessions/revoke` drops every attached client; containers keep running. **M**
- [ ] `clients` history reflects attach/detach. **M**
- [ ] Headless agent session route returns 409 (not attachable). **A**

## 7. Port broker (host file access)

- [ ] `port` grant/revoke scopes; deny-all by default. **A** (`port_trade_test.go`)
- [ ] `intake` / `export` / `write` honor scope + RW vs RO. **A**
- [ ] An island cannot reach host content outside its granted scopes. **A**
- [ ] Scope grant/revoke is operator-only — never self-service from inside an island. **A** (token path; socket no longer mounted, §0)
- [ ] `cp` in/out of an island; `exec` one-shot command. **M**

## 8. Autonomy / Home Island / spawn

- [ ] Home Island (`home`, role=home) is headless + gets `DEJIMA_HOME`. **A**
- [ ] In-island → daemon over the token path is island-scoped (can't touch another island). **A**
- [ ] Home token can create child islands; spawn returns the child's bearer token. **A**
- [ ] Operator-driven create does **not** print a child token. **A**

## 9. Credentials — Claude

- [ ] Host login (Keychain/file) seeds new islands; `auth push` works when the daemon is remote/headless. **M**
- [ ] `auth status` reports host source + seed presence without leaking the secret. **A** (status shape) / **M**
- [ ] Mounted read-only into the island; `reset` re-seeds. **M**

## 10. Credentials — GitHub identities (new)

- [ ] `auth push --github [--name] [--default]` seeds the daemon from the active gh account. **M**
- [ ] `auth status` lists identities (login@host, default marker) — never tokens. **A** (no-leak) / **M**
- [ ] `GET/PUT/DELETE /v1/credentials/github` CRUD; list omits tokens. **A**
- [ ] Identity store is atomic + locked (no lost updates / torn reads under concurrency). **A**
- [x] Repo browse via daemon (`/repos`) works from a device with no gh; Enterprise host via `/api/v3`. **A** (handler + client) / **M** (live)
- [ ] Create with `--github-identity` / TUI picker; unknown identity → 400; empty → default → host gh fallback. **A**
- [x] Chosen identity materializes the gh config mounted at `/opt/host/gh-config`; **git push inside the container actually authenticates as it**. **M** (task #3, verified live on Minion 2026-06-17: clone + push to aoos/kiloton authed as `work`). Required a fix — the materialized config must be in gh's already-migrated schema (`users:` map + `config.yml` version marker), else gh's first-use migration write fails on the read-only mount and `gh auth setup-git` leaves no credential helper (commit `fix(github): materialize gh config in migrated schema`).
- [ ] Commit **authorship** matches the selected identity, not the host gitconfig. **GAP** (task #19): live push authored as `arachlin@gmail.com` (from `/opt/host/gitconfig`) while authenticated as `work`/aoos — git author should derive from the identity.
- [ ] Per-island token removed on island delete. **A**
- [x] Deleting an identity an island uses warns rather than silently changing/losing auth (`affected_islands`). **A** (task #4)
- [x] `auth push --github` token is validated before storing (GET /user). **A** (task #5)
- [x] Repo browser indicates when the list is capped (`Capped` from the Link header). **A** (task #9)
- [x] Enterprise host seedable via `auth push --github --host`. **A** (unit) / **M** (live, task #8)

## 10b. Host terminals (operator-only, `--host-terminals`)

- [ ] Routes absent from the token-auth allow-list — island tokens get 403. **A** (`tokenauth_test.go`)
- [ ] Off by default; `dejimad --host-terminals` (or `DEJIMAD_HOST_TERMINALS=1`) enables; startup logs a warning. **A** (gate) / **M** (warning)
- [ ] Create/list/relabel/delete CRUD; registry persists at `~/.dejima/host-terminals.json`. **A** (integration)
- [ ] TUI "Host · not contained" section; `t` creates+attaches; `d` kills; detail warns "NOT contained". **M** (§L3)
- [ ] Terminal is a host tmux session `dejima-term-<id>`; survives disconnect + daemon restart; resumable from another device. **M** (§L3)

## 11. Events & webhooks

- [ ] `webhook` subscribe/list/unsubscribe; events delivered on state change. **M**
- [ ] Per-agent shims emit events via the internal endpoint. **M**
- [ ] `GET /v1/islands/{name}/events` stream. **M**

## 12. Audit & ledger

- [ ] `audit` / `GET /v1/audit` surfaces bridge transactions. **A** (ledger pkg) / **M** (cli)

## 13. Image build & upgrade

- [ ] `image build` on the daemon host (no source checkout needed). **M**
- [ ] First-island auto-build when the image is absent. **M**
- [ ] `overview` reports image presence. **M**

## 14. CLI surface (per-command smoke)

Each command runs, `--help` is sane, errors are clean (not panics): `init`,
`home`, `connect`, `ls`, `agent`, `status`, `hibernate`, `wake`, `reset`,
`purge`, `upgrade`, `exec`, `cp`, `port`, `audit`, `logs`, `image`, `service`,
`webhook`, `auth`, `logout-all`, `clients`, `overview`, `doctor`, `onboard`,
`tui`. **M**

## 15. Cross-cutting

- [ ] Bad input is a clean 4xx, never a 5xx/panic, across handlers. **A** (partial)
- [ ] Secrets never appear in logs, list responses, or operator terminals. **A** (gh list) / **M**
- [ ] Anything written under `/workspace` (and `~/.dejima` state) persists across container/daemon restart. **M**
- [ ] `go test ./...`, `go vet ./...`, `gofmt` all clean; `-race` clean (needs cgo). **A**
- [ ] Platform matrix: Linux daemon (native Docker) + macOS daemon (VM engine, unix-socket constraints) both exercised. **M**

---

## Live verification procedures (host-gated — run on the daemon host)

These need a **real container engine** and can't run in a dev island (no Docker).
Run them on Minion (macOS) unless a step says otherwise. Each maps to a checklist
row above and/or an open task. Tick the row when the step passes.

### §L1 — In-island routing over the token path (secure-island-routing)
*Verifies §0 / §2. The highest-stakes test: it changed the containment boundary.*
1. Start/refresh the daemon (token listener is on by default at `127.0.0.1:7274`;
   pass `--token-tcp <addr>` only to override).
2. Create an island and attach an interactive agent.
3. Confirm agent state still flows to the TUI/`events` stream (a banner/idle
   transition appears). This exercises `agent-event` over the **token path**,
   since the control socket is no longer mounted.
4. Negative: from **inside** the island, try to reach a control route, e.g.
   `curl --unix-socket /run/dejima.sock http://x/v1/islands` → must fail (socket
   absent), and a token-bearing call to a non-allow-listed route (e.g. create
   island) → `403`. Confirms the hole is closed live, not just in tests.
5. On macOS the container reaches the host via `host.docker.internal`; confirm
   no `--token-tcp` wildcard/LAN bind is in use (loopback only).

### §L2 — In-container GitHub auth / `git push` (task #3)
*Verifies §10 line 115 — the one identity row still marked `!`.*
1. `dejima auth push --github --name work --default` from a host that has `gh`
   logged in (seeds the daemon identity store).
2. Create an island selecting that identity (TUI GitHub browser, or
   `dejima init --repo <url> --github-identity work` — `--repo` is a flag and
   wants a full clone URL, e.g. `https://github.com/<owner>/<repo>`; there is no
   `owner/repo` shorthand, and a local path won't seed against a remote daemon).
3. Inside the island: `gh auth status` → authenticated as the expected login;
   `cat /opt/host/gh-config/hosts.yml` present and single-identity.
4. Make a trivial commit and `git push` → succeeds, authored/authenticated as the
   chosen identity (not the host's default). This is the real end-to-end gap.
5. Delete the island → confirm `~/.dejima/secrets/github/islands/<name>` and the
   per-island token are gone (§3 / §10 line 116).

### §L3 — Host terminals live (host-terminals)
*Verifies §10b. Needs `tmux` on the daemon host.*
1. Start the daemon with `--host-terminals`; confirm the startup warning.
2. In the TUI, `t` → a new host terminal opens and attaches; run `hostname` /
   `whoami` → it's the **host**, not a container.
3. Detach, then reattach (`⏎`) from a second device → same live session.
4. Restart the daemon → the `dejima-term-<id>` tmux session survives; reattach.
5. `d` closes it (confirmed) and the tmux session is gone.
6. Negative: an in-island token hitting `/v1/terminals` → `403` (also **A**).

### §L4 — Native-Linux token-listener reachability (task #12)
*Linux daemon only — the known caveat. Verifies §2 line for the token listener.*
1. On a **native-Linux** daemon host, start dejimad (token listener defaults to
   `127.0.0.1:7274`).
2. Create an island; from inside it, try to reach the host token endpoint via the
   bridge gateway (`host.docker.internal` / `172.17.0.1`).
3. **Expected today: this can fail** — a loopback (`127.0.0.1`) bind on the host
   is not reachable from the container across the bridge. Record the outcome.
4. If it fails, the fix options are: bind the token listener to the docker bridge
   gateway (e.g. `--token-tcp 172.17.0.1:7274`) instead of loopback, or add a
   host-gateway alias and bind there. Confirm one works, then decide the default
   for Linux (macOS is unaffected — `host.docker.internal` resolves to the VM host).

---

## Coverage snapshot (today)

Automated tests exist for: `internal/api` (islands/agents/port/tokenauth/github),
`internal/project`, `internal/githubid`, `internal/reposrc`, `internal/handlers`,
`internal/porttoken`, `internal/ledger`, `internal/islandimage`,
`internal/agentcreds`, `internal/version`, `cmd/dejima`, `cmd/dejimad`. Live
end-to-end lives in `scripts/integration.sh`.

Biggest gaps right now (all **host-gated**, see *Live verification procedures*):
live in-island routing over the token path (§L1), in-container GitHub auth /
`git push` (§L2, task #3), host-terminal attach (§L3), native-Linux token
reachability (§L4, task #12), and the broad **M**-only CLI/lifecycle paths that
only `scripts/integration.sh` exercises. The unix-socket containment hole is
**closed in code** (`secure-island-routing`); only its live confirmation remains.
