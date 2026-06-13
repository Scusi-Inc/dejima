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

- [ ] **!** In-island code cannot reach the daemon control plane. The Linux unix
  socket currently serves the full unauthenticated `routes()` mux and is mounted
  into every island — a contained agent can self-grant Port scopes, create/delete
  islands, and read/tamper with GitHub identities. (task #1) **M/!**
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

- [ ] Unix socket listener (Linux): control-plane reachable only as intended. **!** (see §0)
- [ ] Tailnet TCP listener: only tailnet IPs accepted; off-tailnet refused. **A** (tokenauth scope) / **M** (bind)
- [ ] Host-internal token-TCP listener (macOS autonomy): default-deny, island-scoped. **A** (`tokenauth_test.go`)
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
- [ ] `SetPrimaryID` unit-covered (rename + tmux derive, no-op cases). **!** (task #7)
- [ ] List rows lead with the name (label, or id when unlabeled); detail view shows the id handle. **A**
- [ ] Duplicate labels don't render two identical rows. **!** (task #10)

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
- [ ] Scope grant/revoke is operator-only — never self-service from inside an island. **A** (token path) / **!** (socket, §0)
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
- [ ] Repo browse via daemon (`/repos`) works from a device with no gh; Enterprise host via `/api/v3`. **A** (client) / **M** (live)
- [ ] Create with `--github-identity` / TUI picker; unknown identity → 400; empty → default → host gh fallback. **A**
- [ ] Chosen identity materializes one `hosts.yml` mounted at `/opt/host/gh-config`; **git push inside the container actually authenticates as it**. **!** (task #3, live)
- [ ] Per-island token removed on island delete. **A**
- [ ] Deleting an identity an island uses warns rather than silently changing/losing auth. **!** (task #4)
- [ ] `auth push --github` token is validated before storing. **!** (task #5)
- [ ] Repo browser indicates when the list is capped (100). **!** (task #9)
- [ ] Enterprise host seedable via `auth push --github --host`. **!** (task #8)

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

## Coverage snapshot (today)

Automated tests exist for: `internal/api` (islands/agents/port/tokenauth/github),
`internal/project`, `internal/githubid`, `internal/reposrc`, `internal/handlers`,
`internal/porttoken`, `internal/ledger`, `internal/islandimage`,
`internal/agentcreds`, `internal/version`, `cmd/dejima`, `cmd/dejimad`. Live
end-to-end lives in `scripts/integration.sh`.

Biggest gaps right now: the unix-socket containment hole (§0/§2/§7), no live test
of in-container GitHub auth (§10), and a lot of **M**-only CLI/lifecycle paths
that only `scripts/integration.sh` partially covers.
