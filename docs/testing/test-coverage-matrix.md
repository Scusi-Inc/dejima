# Test coverage matrix — every Dejima feature that needs testing

The master inventory of testable behavior across **CLI, TUI, API, daemon, and live
islands**, derived from the actual surface (every `cmd/dejima` command, all 88 OpenAPI
operations / 65 routes, every TUI key action). This is the spine of the automated harness
([`automated-test-harness.md`](automated-test-harness.md)) **and** the manual Minion pass:
each item maps to the tier that should own it; the Lane 6 agent fills in real status.

**Tier:** `T1` CI unit/handler/`teatest` (no Docker) · `T2` Docker integration · `T3`
macOS-host (Mac mini) · `T4` real-agent smoke.
**Now (current coverage, conservative — Lane 6 reconciles):** `A` automated (unit or
`integration.sh`) · `A*` **wired** in the Tier-3/Tier-4 suite, awaiting its first live
Mac-mini run (`scripts/tier3/*.sh`, `scripts/tier4/agent-smoke.sh` via the nightly
`workflow_dispatch`) · `M` manual verify today · `▢` none yet.

---

## 1. Island lifecycle
- [x] `init` / `new` / `create` — create an island from a repo · CLI/API/TUI · T2 · A
- [ ] create from a private repo (GitHub creds) · T2/T4 · A* (tier4/agent-smoke.sh; bot repo)
- [x] `ls` / `list` — list islands + states · CLI/API/TUI · T1/T2 · A
- [x] `status <name>` / `info <island>` — status + details · CLI/API · T2 · A
- [ ] `GET workspace-ready` — readiness gate · API · T2 · ▢
- [x] `clone <name> <new-name>` — duplicate an island · T2 · A
- [x] `hibernate <name>` — stop container, keep state · CLI/API/TUI · T2 · A
- [x] `wake <name>` — resume hibernated island · T1/T2 · A
- [ ] `reset <name>` — reset island · T2 · M
- [x] `upgrade [name]` — re-pull base image / migrate · T2 · A
- [x] `purge <name>` — destroy island · CLI/API/TUI · T2 · A
- [x] purge **unpushed-work guard** — refuse/confirm when commits unpushed · T2 · A
- [x] `force-purge` / `purge-force` — override the guard · TUI/CLI · T2 · A
- [ ] `recreate-island` (TUI) — recreate after OOM/resource change · T3 · M
- [ ] `relabel`/`rename-island` — change title · CLI(PATCH)/TUI · T1/T2 · M
- [ ] `resources` (PUT) — set per-island memory/CPU/OOM-priority · T2/T3 · M
- [ ] island survives daemon restart (state persists) · T2 · M

## 2. Agents
- [x] `agent add <island>` — add an agent (own git worktree/branch) · CLI/API/TUI · T2 · A
- [x] `agent ls/list <island>` — list agents + states · CLI/API · T2 · A
- [ ] `agent rm <island> <agent-id>` — remove agent (worktree cleanup) · CLI/API/TUI · T2 · M
- [ ] `agent config` get/set (`PATCH .../config`) · CLI/API · T2 · M
- [ ] `relabel-agent` / `agent relabel` — rename · TUI/CLI · T2 · M
- [ ] `agent open <island> [id]` — open agent UI/url · CLI · T2 · M
- [x] `agent-types` / `types` — capability discovery · CLI/API · T1 · A
- [ ] per-agent **idle-seconds** metric emitted · API(`/metrics`) · T2 · A
- [ ] agent worktree isolation (no cross-agent file collision) · T2 · M

## 3. Sessions, terminals, attach
- [ ] `attach <id>` / island `session` (WS) — interactive attach · API/CLI · T2/T3 · M
- [ ] per-agent `session` (WS) · API · T2 · M
- [ ] **terminal auto-reconnect** — drop link (daemon restart / sleep-wake) → reattaches, doesn't close · T3 · A* (tier3/safe.sh)
- [ ] multi-attach (two clients, same session) · T3 · M
- [ ] terminal resize propagation · T3 · M
- [x] `exec <name> -- <cmd>` — one-shot exec · CLI/API · T2 · A
- [ ] host `terminals` create/ls/rm/relabel + `session` (owner-only) · CLI/API/TUI · T3 · M
- [ ] clean detach (Ctrl-b d) exits without killing the agent · T3 · M

## 4. Repo / git integration
- [ ] clone repo into island on create · T2 · M
- [ ] `push` — push island work · CLI · T2/T4 · A* (tier4/agent-smoke.sh; lenient)
- [ ] GitHub identity used for clone/push (per-island) · T4 · A* (tier4/agent-smoke.sh; bot identity)
- [ ] unpushed-work detection (feeds the purge guard) · T2 · M

## 5. Port (host-file brokering)
- [ ] `port grant <island> <host-path>[:ro|:rw]` — operator grants host access · CLI/API · T2 · A
- [ ] `port revoke` · T2 · A
- [ ] `port scopes` (list own grants) · CLI/API · T2 · A
- [ ] `port intake <island> <scope>:<path>` — pull host→island · T2 · A
- [ ] `port export <island> <container-path>` — island→host · T2 · A
- [ ] `port write <island> <container-path> <scope>:<path>` (RW) · T2 · A
- [ ] path-traversal refusal (../, symlink escape) · T2 · A
- [ ] read-normalization (0600 host file readable by uid 1000) · T2 · A
- [ ] Ledger entries + `--verify` (hash chain) · T2 · A
- [ ] deny-all default (no grant → refused) · T2 · A
- [ ] macOS Shortcuts intake path · T3 · M

## 6. Capabilities
- [ ] `cap grant/revoke/list <island> <target>` · CLI/API · T2 · A
- [ ] `capabilities/execute` (in-island token) · API · T2 · A
- [ ] deny-all default + `capability.*` ledger · T2 · A
- [ ] Linux `script` adapter · T2 · A
- [ ] macOS Apple Shortcuts adapter · T3 · M

## 7. MCP brokering
- [ ] `mcp grant/revoke/list <island> <server>` · CLI/API · T2 · A
- [ ] `mcp call <island> <server>` / `mcp/call` (in-island token) · API · T2 · A
- [ ] deny-all default + `mcp.*` ledger · T2 · A
- [ ] stdio broker spawns/relays the server · T2 · M

## 8. Inter-island exchange (Lane 5) — see operator-tests/inter-island-wave.md
- [ ] `link grant/revoke/ls` (island→island + topic) · CLI/API · T2 · A
- [ ] deny-all default (no grant → refused + `link.deny`) · T2 · A
- [ ] `link send` (info) → delivered into recipient mailbox · T2 · A
- [ ] `msg send/poll` (intra-island mailbox) · CLI/API · T1/T2 · A
- [ ] island broadcast (`to` empty) · T2 · A
- [ ] structured provenance `Origin{source_island,cross_island}`, unforgeable · T1/T2 · A
- [ ] `link expose/unexpose/exposed` (action types) · CLI/API · T2 · A
- [ ] `link action` request — deny-all + `{B exposes} ∩ {grant.Actions}` · T2 · A
- [ ] pre-authorized action → executes immediately · T2 · A
- [ ] non-pre-authorized → `pending` + `link.action-pending` webhook · T2 · A
- [ ] `link approvals/approve/deny` (operator only) · CLI/API · T2 · A
- [ ] **agent can never self-approve** (token listener lacks the route) · T1/T2 · A
- [ ] gate re-checked at approval time (revoked grant → refused) · T1 · A
- [ ] fail-closed: TTL expiry + queue dropped on daemon restart · T1 · A
- [ ] `link.action`/`link.deny`/`link.approve` ledgered w/ actor · T2 · A
- [ ] **wake-on-message** (P3.5): idle agent nudged at turn boundary · T3 · ▢ (needs real-adapter live run; tier4 exercises the inject seam)
- [ ] wake: busy agent NOT interrupted mid-turn · T3 · ▢ (real-adapter live run; the key P3.5 unknown)
- [ ] wake: hibernated recipient island wakes on message · T3 · ▢ (real-adapter live run)
- [ ] hard-interrupt routed through the action gate (not a flag) · T3 · ▢
- [ ] `mailbox.arrival` event carries flags only (no body) · T1 · A
- [ ] recipient acts only on daemon-stamped `Action` messages · T4 · ▢

## 9. Audit & activity
- [ ] `audit` record (api.request + lifecycle) · API · T2 · A
- [x] `audit` filter (actor/type/island/time) · CLI · T2 · A
- [ ] `audit --verify` (tamper-evident hash chain) · T1/T2 · A
- [ ] `audit --export jsonl|csv` · T2 · A
- [ ] optional HMAC keying · T2 · M
- [ ] `activity` feed (curated, who+agent did what) · CLI/API · T2 · A
- [ ] identity attribution (actor/role on each record) · T1/T2 · A
- [x] TUI audit pane (`A`) · T1(teatest) · A

## 10. Team auth, tokens, roles
- [ ] `token` create/ls/rm (`/v1/tokens`) — owner only · CLI/API · T1/T2 · A
- [ ] roles owner > operator > viewer enforced per route · T1 · A
- [ ] per-island token scope (in-scope ok, out-of-scope denied) · T1/T2 · A
- [ ] scoped token denied global ops; allowed global reads · T1 · A
- [ ] viewer denied `purge`, denied link `approve` · T1/T2 · A
- [ ] in-island token: own island only, never another · T1 · A
- [ ] present-but-unknown token → 401 (not trusted-owner) · T1 · A
- [ ] `--require-token` makes no-token a hard 401 · T1 · M
- [ ] exchange-down (no-token on trusted listener = owner) · T1 · A

## 11. Credentials
- [ ] `auth` claude set/status (`credentials/claude`) · CLI/API · T2 · M
- [ ] github set/list/rm + `/repos` (`credentials/github`) · CLI/API · T2/T4 · M
- [ ] `provider set/rm/list` (masked, no keys leaked) · CLI/API · T2 · M
- [ ] **Keychain** storage on macOS (no plaintext in config) · T3 · A* (tier3/safe.sh)
- [ ] per-agent provider key injection (`<PROVIDER>_API_KEY`) · T2/T4 · M
- [ ] missing-key health surfaced · T2 · M

## 12. Webhooks & events
- [ ] `webhook subscribe` (`events/subscribe`) — url/secret/event · CLI/API · T2 · M
- [ ] `events/subscriptions` list/rm · CLI/API · T2 · M
- [ ] event stream (`/v1/islands/{name}/events`, SSE/WS) · API · T2 · M
- [ ] HMAC `X-Dejima-Signature` on delivery · T2/T3 · M
- [ ] webhook secret stored in Keychain (no plaintext) · T3 · A* (tier3/safe.sh)

## 13. Onboarding & host setup (macOS)
- [ ] `onboard` adaptive first-run (configured/unreachable/fresh-host/generic) · T1/T3 · M
- [ ] `onboard` connection-failure offer (one-shot troubleshooter) · T3 · M
- [ ] `onboard --provision-host` — full 6-phase wizard · T3 · A* (tier3/system.sh; idempotent --yes, opt-in)
- [ ]   …disables sleep (pmset), Remote Login, PATH · T3 · M
- [ ]   …installs Homebrew/Tailscale/Docker (idempotent, resumable) · T3 · A* (tier3/system.sh asserts no re-install)
- [ ]   …`service install --system --audit` + reboot survival · T3 · A* (tier3/system.sh; reboot double-gated)
- [ ]   …`--yes` non-interactive + `--reset` · T1/T3 · A(unit)/M
- [ ] `doctor` (+ `--fix` undersized Docker VM #23) · CLI · T3 · M
- [ ] `connect <name>` / `enroll` — client enrollment · CLI · T3 · M

## 14. Service / daemon / admin
- [ ] `service install/uninstall/restart` (--system/--user/--tcp/--token-tcp/--audit) · T3 · A* (tier3/system.sh; install→verify→uninstall, opt-in)
- [ ] `update` client + daemon (gates on releases; source = pull+make+restart) · T3 · M
- [ ] `admin/update` route · API · T3 · M
- [ ] `image build` / `image` · CLI/API · T2 · M
- [ ] `panic` engage/clear/status · CLI/API/TUI · T2 · M
- [ ] `healthz`, `overview`, `clients` · API · T1/T2 · M
- [ ] `sessions/revoke` (owner) · API · T3 · M
- [ ] `/metrics` (Prometheus) · API · T1/T2 · A
- [ ] idle auto-hibernate (`DEJIMAD_IDLE_HIBERNATE`) fires + wakes · T3 · A* (tier3/safe.sh)

## 15. SSH façade
- [ ] `ssh authorize/revoke` account-keys (`ssh/account-keys`) · CLI/API · T3 · M
- [ ] shell + sftp land in `/workspace` · T3 · A* (tier3/safe.sh plumbing; full landing via tier3/system.sh --ssh install)
- [ ] VS Code Remote-SSH · T3 · M

## 16. Home Islands & adapters
- [ ] `home create` (--agent openclaw) self-installs + idles (no crash-loop) · T2/T3 · M
- [ ] home-role gate (attachability) · T2 · M
- [ ] OpenClaw `--bind loopback` launch · T3 · M
- [ ] Letta / Hermes / Goose adapters install+launch (live) · T2/T4 · ▢
- [ ] in-island token autonomy reachability (#8, host.docker.internal:7274) · T3 · M

## 17. TUI (teatest unit + live smoke)
- [x] island list + navigation (j/k/up/down/pgup/pgdn) · T1 · A
- [x] agent menu, drill-in/out · T1 · A
- [x] confirm pop-up renders centered + requires typing the island/agent **name** · T1 · A
- [ ] confirm covers uncommitted/unpushed warning in the same pop-up · T1/T3 · ▢
- [x] action menu (`m`): hibernate/wake/reset/upgrade/purge/recreate/relabel/remove-agent/setup-ssh · T1 · A
- [ ] manual deletion of agents/islands via menu (no stall) · T1/T3 · M
- [x] `A` audit pane · `U` update (client/daemon) · `S` setup-ssh · `?` help · T1 · A
- [ ] new-tab launches with manual names · T1/T3 · M
- [ ] live TUI smoke over a real session (tmux+expect) · T3 · ▢

## 18. SDK & clients
- [ ] Python SDK tests (pytest) · T1 · A
- [ ] TS SDK tests · T1 · A
- [ ] OpenAPI route-parity (server ↔ openapi, 88 ops) · T1 · A
- [ ] PTY JSON-envelope+base64 protocol · T1/T2 · A
- [ ] PyPI/npm publish (tag-driven) · T1 · ▢ (needs secrets)

---

## Rollup
~150 line items across 18 areas. **T1** (CI, every PR) and **T2** (Docker) cover the bulk;
**T3** (Mac-mini runner) owns the macOS-host + live-session items currently marked `M`/`▢`;
**T4** is the small real-agent smoke. The biggest gaps today (`▢`) are the **TUI** (no
`teatest` yet), **wake-on-message** (P3.5, just built), **Keychain/idle-hibernate**, and the
**framework adapters** — these are the priority for Lane 6 + the first Mac-mini nightly.
