# Dejima Roadmap

**Last updated:** 2026-06-14

This is the living roadmap for Dejima. Items are grouped by phase and sized roughly. Status legend: `[x]` = built, `[~]` = in progress, `[ ]` = pending.

**Phases ≠ versions.** The `v1` / `v1.x` / `v2` headings are *planning buckets*, not release numbers. Released builds follow semver: the **first public release is `v0.1.0`**, and we stay in **0.x** — where the CLI/API may still break and `api_version` may bump — until we deliberately commit to API stability at **`v1.0.0`** ("safe to build on"). `api_version` (an integer client/daemon contract) is tracked separately from the semver tag.

---

## 🧑 Operator verification queue (built, needs a live run)

These shipped to `master` with unit/security review but can't be exercised from the
build island (no live Docker/macOS host here). Run them on Minion and feed findings back.

- [ ] **OpenClaw `--allow-unconfigured` idles, not crash-loops.** Create a Home Island
  (`--agent openclaw`) with an empty `/workspace` config; confirm the gateway waits
  instead of exiting nonzero / restart-looping in `dejima logs`. If OpenClaw rejects the
  flag, report the correct one. (commit `30d898c`, `internal/handlers/handlers.go`)
- [ ] **#8 macOS TCP autonomy reachability probe** (`runbook-openclaw-home-island.md §5.2`).
  `dejimad --token-tcp 127.0.0.1:7274`; from inside an island confirm
  `host.docker.internal:7274/v1/healthz` reaches the loopback-bound daemon and that an
  in-island `dejima port intake …` authenticates (200, not 401/403). Decides whether
  `host.docker.internal` is the right `--autonomy-dial` on Minion's Docker runtime.
- [ ] **#9 SSH-façade live + VS Code Remote-SSH.** `dejimad --ssh :2222` (prefer a tailnet
  bind), `dejima ssh authorize <island> --key …`, then `ssh <island>@host -p 2222` (lands
  in the container, PTY/resize/exit-codes work) and point VS Code/Cursor Remote-SSH at it
  (server bootstraps over the exec channel; can edit `/workspace`). Note whether sftp is
  needed for your editor flow.

---

## 🌿 Recently merged feature work (now on `master`)

Three feature branches are built, unit/security-reviewed, and **merged to
`master`** (merge commits `57ecb32`, `d991bc4`, `8fee087`). The merge restored
the SSH-façade path helpers that the UX branch's GitHub-identity edit had
displaced in `internal/paths`, so the live SSH-façade (#9) is intact. The
remaining items per branch are a self-generated backlog (live-verify + polish),
none blocking.

### `feat/island-ux-fixes` (merged `8fee087`) — agent ids, name-first rows, GitHub identities
- Island-letter agent ids (`p1`,`p2`…; primary via `SetPrimaryID`; legacy keeps `a1`). Agent rows lead with name (id when unlabeled).
- Add-island repo picker: paste-URL row + daemon-backed "Browse GitHub" (pick identity → repo).
- **Per-daemon GitHub identities** end-to-end: `internal/githubid` (atomic+locked store), `GET/PUT/DELETE /v1/credentials/github[/repos]`, per-island `hosts.yml` mounted at `/opt/host/gh-config` (fallback to host `~/.config/gh`; removed on island delete), `dejima auth push --github` / `auth status` / `dejima init --github-identity`. Docs: `docs/github-identities.md`.
- [ ] Open: live in-container `git push` test · warn on dangling identity ref · verify token on push · `handleGitHubRepos` handler test (base-URL seam) · `SetPrimaryID` unit test · Enterprise host in auth push · "N of M" repo-cap indicator · disambiguate duplicate-label rows.

### `feat/secure-island-routing` (merged `57ecb32`) — close the in-island control-plane hole
- Fixes a critical pre-existing containment hole: the operator unix socket was bind-mounted into every Linux island. Now the control socket is **not** mounted; islands reach the daemon only over the token-authenticated, island-scoped TCP path; `agent-event` moved onto it (authenticated, anti-spoof); `host.docker.internal:host-gateway` added. Docs: `docs/secure-island-routing.md`.
- [ ] Live-verify: in-island telemetry/autonomy over the token path on Minion (macOS).
- [ ] Open: native-Linux token-listener reachability (loopback bind unreachable via the bridge gateway).

### `feat/host-terminals` (merged `d991bc4`) — operator host terminals
- Uncontained operator shells in tmux on the daemon host (humans, **not** agents — "agent ⇒ always a container"). `internal/hostterm` + `internal/bridge` host PTY; `/v1/terminals*` operator-only (`dejimad --host-terminals`, off by default, audited, island-token-denied by test); TUI "Host · not contained" section (`t` to create+attach). Docs: `docs/host-terminals.md`.
- [ ] Live-verify: interactive attach on a daemon with `--host-terminals` + tmux installed.
- [ ] Roadmap: `dejima term` CLI intentionally deferred (kept TUI-only to bound the most-privileged surface).

---

## v1 (current — dogfood phase)

The v1 vertical slice. Buildable, testable. Real-world dogfood pending.

- [x] M0 — Foundation: Go module, repo skeleton, CI on macOS + Linux
- [x] M1 — MVP island + daemon (CLI: `init`, `connect`, `ls`, `status`, `purge`; Unix-socket API; Dockerfile)
- [x] M2 — Lifecycle: `hibernate`, `wake`, `reset`; daemon adopts existing islands at startup
- [x] M3 — Multi-attach session via websocket API + presence
- [x] M4 — Service install (launchd/systemd); Tailscale-pinned TCP listener; webhooks; per-agent shims (Claude Code installed)
- [x] M5 — Resource caps; `exec` / `cp` / `logs` access verbs; multi-agent disambiguation
- [x] **Codex CLI as a bundled agent** — second agent shim, per-agent state volume mount, honest "agent-agnostic" claim
- [~] M6 — Dogfood on Mac mini for one week; document rough edges

---

## 🚢 Release 0.1.0 — the cut line

Everything in this section gates the **first public release** — the moment we open Dejima to others. Nothing below this section blocks shipping 0.1.0.

- [ ] **🧑 USER TASK — Push the repo to `github.com/aoos/dejima`** so install URLs resolve. (5 min)
- [ ] **🧑 USER TASK — Enable GitHub Pages from `master:/(root)`** so `aoos.github.io/dejima/install.sh` works. See [`docs/distribution.md`](distribution.md). (5 min, free)
- [ ] **🧑 USER TASK — Create `aoos/homebrew-dejima` tap repo + copy `homebrew/dejima.rb` to `Formula/dejima.rb`**. (1 hour, free)
- [x] **GitHub Releases producer** — `.github/workflows/release.yml` (on `v*` tags) + `make release-binaries`: cross-builds darwin+linux (arm64/amd64) carrying `dejima`+`dejimad`, and Windows client zips, with `SHA256SUMS`, published via `softprops/action-gh-release`. Fires on the first tag once the repo is pushed. *(machinery built + verified locally; unsigned — see notarization)*
- [ ] **macOS notarization — DEFERRED (not a hard 0.1.0 blocker).** 0.1.0 can ship *unsigned*: `brew` and `install-client.sh` strip the Gatekeeper quarantine, so early-adopter friction is minor. Notarize as a fast-follow when ready. *Prep is done and waiting:* `make release-binaries` codesigns darwin when `CODESIGN_IDENTITY` is set, and [`docs/release-notarization.md`](release-notarization.md) is a drop-in runbook (Apple cert + API key + 6 Actions secrets + the macOS-runner workflow diff). (half day, mostly Apple-side, whenever)
- [x] **Version + `api_version` + skew detection** — daemon advertises its build version and `api_version` on `/overview`; client compares its compiled-in value and warns on mismatch (in `doctor` and the TUI footer) instead of silently degrading (the `seed_path` lesson). Semver tags drive the build version via `VERSION`. *(done)*
- [~] **Release consumers — Homebrew formula + client binary installer** — *templates drafted*: `homebrew/dejima.rb` is now a binary formula (per-platform tarball URLs + placeholder `sha256`s, HEAD fallback), and `install-client.sh` is a client-only `curl | bash` that downloads the matching release asset and checksum-verifies it. **Remaining:** fill the four `sha256`s from the first v0.1.0 `SHA256SUMS` (the release CI can auto-bump the tap), and smoke-test both against the live release. `install.sh` stays the *source* path for the full server (it needs the Dockerfile + service scripts). (≈1 hour, right after the first release)

---

⬇ **Everything below ships *after* the 0.1.0 release.**

## v1.x — post-release hardening

Targeted fixes and quality-of-life additions. Sized in hours unless noted.

- [x] **Inter-island network isolation** — each island gets its own user-defined Docker bridge network so containers can reach the internet but not each other. (hours)
- [x] **`dejima doctor`** — single command health check: daemon, Docker, image, Tailscale, every project's container/volume/network/config, webhook subscriptions. (hours)
- [x] **Resource visibility in `dejima status`** — memory + CPU pulled from `docker stats`. (hours)
- [x] **`dejima service install --notify <url>`** — install AND auto-subscribe a notification webhook in one step. (hours)
- [x] **Multi-arch image build target** — `make image-multiarch` for arm64+amd64 publishes via `docker buildx`. (hours)
- [x] **`make install` / `make uninstall`** — copies binaries to `/usr/local/bin`. (minutes)
- [x] **`make setup` bootstrap script** — interactive first-run: detects Docker, offers Docker Desktop via Homebrew (mentions OrbStack/colima alternatives), builds, installs, builds image, registers service. (hours)
- [x] **One-liner installer** — `curl … | bash` that bootstraps Go + clones source + runs `make setup`. (hours)
- [x] **GitHub Pages site (`index.html` + `install.sh` at repo root + `.nojekyll`)** — works at `aoos.github.io/dejima/` once Pages is enabled. (hours)
- [x] **Homebrew formula (`homebrew/dejima.rb`)** — HEAD-only build-from-source formula ready to drop into a `homebrew-dejima` tap repo. (hours)
- [ ] **🧑 USER TASK — Pick + register a custom short domain** (e.g. `dejima.sh`, `dejima.app`). Optional polish on top of GitHub Pages. (~$10-45/yr, 30 min)
- [ ] **Submit to homebrew-core** — eventual `brew install dejima` without the tap prefix. Months of stewardship; defer until v1.x has users. (months)
- [ ] **`dejima update` epic** — one role-aware command that pulls, installs, and restarts (server + client). Consolidates the four former sub-items below.
  - **V1 (dual-mode, local)** — auto-detect: in a git checkout with Go → *source path* (`git pull` + `make install` + restart); else → *release path* (download the `GOOS/GOARCH` asset, checksum-verify against published `SHA256SUMS`, atomic self-replace via go-update/selfupdate — Windows = rename-aside swap since a running `.exe` can't be overwritten). **Role-aware:** client swaps `dejima` only; server swaps `dejima`+`dejimad` then `dejima service restart` (islands survive via `AdoptExisting`; only live attaches blink). **Flags:** `--check` (dry-run/availability), `--yes`, `--channel stable|edge`, `--notify` (fire a webhook before any daemon restart). Reuses release CI + `SHA256SUMS` + `service restart` + events. ~1–1.5d. **Prereq for the *client* half: a release cadence** (even auto-tagged `v0.x` edge builds from CI) — a binary-only client can't build from source.
  - *folds in:* **self-update** (client download+verify+swap → the release path) · **update-available check** (`--check` / daily-opt-in → `update.available` webhook) · **stable vs edge channels** (`--channel`).
  - **Deferred (v2): remote daemon update** — GIZMO → Minion daemon self-update over an *authenticated admin endpoint* (process-restart-behind-launchd + authz; ties to the per-island token work). Notify-then-apply, never silent.
- [x] **Container watchdog goroutine** — daemon polls `Status()`+`Inspect()` every 30s and emits `container.crashed` on unexpected exits (running→stopped/missing) and on restart-count climbs (flapping under `--restart unless-stopped`, e.g. repeated OOM kills); payload carries status/exit_code/oom_killed/restart_count/reason. Edge-triggered (no re-emit at steady state), primes silently on first scan, and stays quiet while panic mode stopped everything deliberately. (`internal/api/watchdog.go`)
- [x] **`dejima upgrade <name>`** — recreate a container against a fresher island image while preserving volumes (also `--all`, and the `u` key in the TUI). Pairs with **`dejima image build`**: the build context is embedded in `dejimad`, so the image rebuilds on the daemon host with no source checkout; missing images auto-build on first `dejima init`.
- [x] **`dejima panic`** — stop every island immediately; write a `~/.dejima/PANIC` flag preventing auto-restart until removed. **Shipped:** `dejima panic` (engage), `--clear` (remove flag + restart islands meant to be running), `--status`; daemon `GET/POST/DELETE /v1/panic`; `AdoptExisting` refuses to auto-start while the flag is set (survives a daemon restart); state surfaced in `/overview`, `doctor`, and a TUI alarm banner. Emits `daemon.panic-engaged`/`daemon.panic-cleared`.
- [~] **Unpushed-work guard on `purge` + `dejima uninstall`** — **guard shipped:** `purge` / `DELETE /v1/islands` now inspect `/workspace` (dirty + ahead-of-upstream) and refuse with 409 unless `--force`, naming the at-risk file/commit counts and branch; a non-running island can't be verified so it also requires `--force`. A blocked TUI purge offers a force-purge confirm. **Remaining:** the one-shot `dejima uninstall` that runs the whole clean-removal sequence (guarded purge of every island → service uninstall → binaries → `~/.dejima`). (hours)
- [x] **`dejima refresh-creds <name>`** — covered by `dejima upgrade <name>`: recreating the container re-assembles all credential mounts (and re-materializes the Claude seed) without touching the workspace. `dejima auth push` handles getting fresh Claude credentials onto the daemon host in the first place.
- [ ] **`dejima clone <name> <new-name>` — copy an island (with its credentials)** — duplicate an island: new config + a copy of its workspace **and** home volumes (throwaway container, `cp -a`). Because all creds / permissions / tool-auth live in the per-island `/home/dejima` home volume ([`multi-agent-spec.md`](multi-agent-spec.md) §6), cloning carries them along for free — so this is the natural "copy an island with everything" primitive, and it also underpins a *true* island rename (clone to a new name + delete the old; today rename is a cosmetic display **title**, since Name is immutable infra identity). Caveats: device/host-bound tokens may not survive a cross-host clone, and duplicating `~/.claude` duplicates session/runtime state (the §6 shared-home note). **Copying *just* credentials/permissions to another island is deliberately out** — it reintroduces the per-tool-token-path enumeration the whole-home volume was chosen to avoid; if ever needed, extend the `dejima auth push` seeding per-tool instead of doing volume surgery. (1-2 days)
- [ ] **Secrets at rest via Keychain / Secret Service** — webhook HMAC secrets and any future dejima-held tokens out of plaintext config. (day)
- [ ] **Idle auto-hibernate** — opt-in threshold (e.g., "hibernate after N hours with no client + no agent process"). (day)
- [ ] **`dejima onboard --provision-host`** — Mac-mini-as-home-server provisioning wizard. Walks through Energy Saver / Sharing / Homebrew / Tailscale / Docker / SSH config / `.zshenv` (auto-doing what it can, instructing for GUI-only steps), then hands off to the existing Dejima onboarding. Closes the "I just unboxed a Mac mini, what now?" gap. **Strategically important: shifts positioning toward "the easy way to turn a Mac mini into a personal AI agent server."** Full plan: [`docs/host-provisioning-plan.md`](host-provisioning-plan.md). (~1 week)
- [ ] **Adaptive first-run prompt** — Detect server vs client context (Docker + dejimad binary present? DEJIMA_HOST set? daemon reachable?) and ask a context-specific y/n/N question instead of the generic "first time?" — e.g., on a server: *"Need help setting this up for remote agent access?"*; on a client: *"Need help connecting to your Dejima host?"*. Same marker / never semantics; smarter framing. (half day)
- [ ] **Connection-failure offer** — When the CLI hits a "daemon unreachable" error for the first time on a host that has `DEJIMA_HOST` set, surface a one-shot *"Want help troubleshooting the connection?"* prompt. Doesn't fire on subsequent transient failures (one offer per session). (half day)
- [ ] **Webhook security hardening** — the URL itself is a secret today. Improvements: (a) require a strong HMAC secret by default rather than as opt-in, (b) optional bearer-token auth on the receiver, (c) generate a high-entropy ntfy topic suffix automatically when user types a bare topic name, (d) interactive secret prompt during `dejima service install`. Already partially shipped (HMAC + interactive secret prompt); the rest is roadmap. (1-2 days)
- [x] **Headless-Mac service install via LaunchDaemon** — shipped in two tiers: `dejima service install` now falls back from the missing gui domain to `launchctl bootstrap user/<uid>` (supervised — KeepAlive crash restarts — for the current boot, no sudo, works over plain SSH), and `dejima service install --system` writes `/Library/LaunchDaemons/dev.dejima.dejimad.plist` (runs as the installing user via `UserName`; sudo for the privileged steps) which loads at boot with **no desktop login ever**. `restart`/`uninstall`/`status` honor `--system`. (done)
- [ ] **Headless boot vs locked login keychain** — a `--system` LaunchDaemon starts dejimad at boot *before any login*, when the user's login keychain is still locked, so keychain-sourced Claude creds can fail until someone SSHes/logs in once per boot. Fix: (a) make the daemon's creds probe detect the locked-keychain case and report it distinctly (doctor fix hint: "log in once, or seed file-based creds" — today it would misleadingly FAIL with "run `dejima auth push`" as if never logged in), (b) on `--system` installs, recommend/offer the file-based seed path (`dejima auth push`), which doesn't depend on the keychain. (half day)
- [ ] **Auto-login detection + recommendation** — during host provisioning, detect whether auto-login is enabled and recommend turning it on for headless Mac mini setups (so LaunchAgents survive reboots without needing a LaunchDaemon). (hours)
- [ ] **Mac mini host setup runbook (`docs/mac-mini-host-setup.md`)** — Companion screenshots-and-paragraphs guide for people who'd rather read than be wizard'd. Shipping cost: ~3 hours. (low effort)
- [x] **Interactive TUI (`dejima` with no args)** — bubbletea dashboard: live state, presence, keyboard nav, single-key lifecycle, a help overlay (`?`), an `n` repo-picker creator, a connection switcher (`s`), and open-in-new-window (`o`). One-shot CLI verbs still work for scripts. (done)
- [ ] **Default-on attach notifications at install** — `dejima service install --notify <url>` becomes the recommended path; first install prompts for a webhook URL. Awareness without surveillance. (hour)
- [ ] **Opt-in audit log (`Ledger`)** — append-only `~/.dejima/ledger.jsonl` of operational events. **Off by default**, never silently enabled, easy to disable. For compliance contexts only. (week)
- [ ] **Opt-in trust-on-first-use** for new clients — paranoid mode for users who want stronger-than-tailnet auth. Off by default. (week)
- [ ] **Opt-in egress allow-list per island** — `network.allow = ["api.anthropic.com", ...]` in project config. Default: open. (day)

---

### Observability — real-time signals for dashboards / wrapper tooling

Dejima's stance: surface rich real-time state via the API and let wrapper tooling own history, aggregation, and per-user/per-org rollups (same division as the built-in-cost-tracking non-goal below). These are the real-time signals still worth exposing.

- [x] **Crash health in island detail** — `oom_killed`, `restart_count`, `exit_code` from `docker inspect`, surfaced on `GET /v1/islands/:name` and in the TUI detail pane. Signals an agent killed by its memory cap or a flapping container — facts a remote client can't observe itself. (done)
- [x] **Per-island disk usage** — workspace + home volume sizes surfaced alongside mem/cpu in `status` and the TUI detail pane. `runtime.VolumeSizes` does one `docker system df -v` call mapped by volume name; the daemon caches it 30s (slower than `docker stats`, disk drifts slowly) and only populates it on the detail endpoint (`IslandInfo.Disk`), never the list. Size reads 0 on storage drivers that don't report it (rendered only when > 0). (`internal/runtime/docker.go`, `internal/api`)
- [ ] **Prometheus `/metrics` endpoint** — islands-by-state, per-island cpu/mem/disk, agent-idle seconds, restart/OOM counts. Makes Dejima ping-able by any org dashboard (Grafana) with zero bespoke UI, and is the natural fan-in point when many daemons report to one place. Strongest single enabler for the multi-employee high-level dashboard. (1-2 days)
- [x] **Agent-process liveness** — distinguishes a crashed *agent* inside a still-running container from a healthy one via the tmux pane command (the cheap path; no shim changes). `agentLiveness` adds an `"exited"` agent state — tmux session alive but its foreground fell back to a bare shell (agent process died while `start.sh` keeps the container up) — alongside `running`/`stopped`. Never fires for the shell agent type (a prompt is its healthy state). Surfaced in `IslandInfo.Agents[].State`, `dejima status`, a red TUI glyph + detail line, and a `doctor` WARN row. Detail-endpoint only. Complements the container watchdog, which only sees container exits. *(Headless/SDK agents — which have no tmux — still need the heartbeat-shim path; tracked under Observability "agent-process liveness" follow-up.)*
- [ ] **Island ownership + tags** — persist a creator label and free-form tags (`--tag team=web`) on each island; surface in `IslandInfo`. Enables per-user / per-team rollups in wrapper dashboards. Storable now even before the auth model lands; pairs with the token/roles work in v2. (half day)
- [ ] **Doctor: daemon supervision check** — doctor currently answers only "is the daemon reachable?", not "*how* is it running?". Detect and report the supervision mode: launchd-supervised (and in which domain — gui/user/system) / systemd-supervised / orphan `nohup`-style process / not running. WARN when the daemon is reachable but won't survive a reboot (orphan, or user-domain agent on a headless Mac), and when a service plist is loaded but its daemon isn't the one answering (e.g. crash-looping because an orphan holds :7273 — the exact failure mode of switching from manual to service mode without `pkill`). Fold `dejima service status` into doctor. (half day)
- [x] **`daemon.started` webhook event** — emitted on dejimad startup with `{version, api_version, listen:[...]}` in the payload. On a headless host this is the only push-shaped way to learn the box rebooted or the daemon crashed and was restarted by its supervisor; pairs with the container watchdog's `container.crashed`. (`Server.EmitDaemonStarted`, fired from `cmd/dejimad`)

---

## v1.x — open design questions

Questions worth answering before committing to an implementation.

- [ ] **Shared workspace volume across islands** — `dejima init --workspace shared:foo` joins an existing workspace volume instead of creating a fresh one. Enables "multiple role-based agents on the same code, each in its own island" without forcing git-roundtrips between them. Open: how to handle merge conflicts when N agents write to the same files; whether agent state stays per-island (yes) or shared (probably no). (open design, 1-2 days when settled)
- [ ] **Multi-island sibling view in TUI / `dejima ls`** — group islands that share a repo (or a workspace volume) visually so multi-agent setups read as one project with N agents. UI-only change. (hours)
- [ ] **Agent-scoped file access** — once islands host N agents on per-agent git worktrees, the file verbs (`GET/PUT …/files`, `dejima cp`, `dejima exec`) become path-ambiguous. The multi-agent MVP just defaults them to the primary worktree (`/workspace`, no new surface). Open question for later: a richer per-agent file surface — `--agent` targeting on `cp`/`exec`/`files`, and possibly a browse/read API over an agent's worktree. (open design, when multi-agent lands)

## Port — brokered host-file access (assistant agents)

Read-only V1 shipped & **validated on live Docker** (`scripts/integration.sh` 38/38, Minion 2026-06-12). Detail: `port-island-spec.md`, `runbook-openclaw-home-island.md`.

- [x] **Phase 0–1** — per-island scopes (deny-all default), hash-chained tamper-evident Ledger, read-only `intake`/`export`, `dejima port …` + `dejima audit --verify`. Unit-tested + validated end-to-end (`scripts/integration.sh` 38/38 on live Docker).
- [x] **Phase 2 core** — Home Island (`role=home`, `dejima home create`) + native-vs-island fork.
- [ ] **Intake read-normalization** — `chmod a+r` the copy after `docker cp` so the island agent (UID 1000) can read host files regardless of host mode. Blocked on smoke-test finding #1 (0600 host files land EACCES). (hours)
- [x] **macOS TCP autonomy path** — brain-driven Port/spawn was **blocked on macOS** (the in-island daemon socket is Linux-only; Minion is macOS). **Shipped:** per-island token (`internal/porttoken`, constant-time) → host-internal `--token-tcp` listener (`assertHostInternalBind` refuses wildcard) → default-deny bearer-auth + island-scoping middleware (`internal/api/tokenauth.go`) → `DEJIMA_HOST`/`DEJIMA_TOKEN` env injection → in-island bearer client → spawn-returns-child-token (parent-child, no god-token). `/security-review` caught + fixed a cross-island authz bypass (encoded-slash path divergence; now authorize on `EscapedPath`+`ValidateName`, and `project.Load` validates names). Mapped in `runbook-openclaw-home-island.md §5`. Remaining is operator-only: the §5.2 live reachability probe on Minion's Docker.
- [~] **Phase 3 — SSH-façade adoption + framework backend adapters** (Hermes/Goose). Docker-daemon emulation **rejected** (`port-island-spec.md §5`). Two-birds: the same island-as-SSH-endpoint is the **VS Code / Cursor remote-dev on-ramp** (Remote-SSH into an island → full editor on the worktree, beside the in-island agent). **Shipped (core):** daemon-side SSH server (`internal/sshfacade`, `golang.org/x/crypto/ssh`) — the daemon is the single SSH front door (username names the island, per-island public-key auth), bridging session channels into the container via `docker exec` (works on macOS + Linux, no in-island sshd, no published ports). `dejimad --ssh <addr>` + `dejima ssh authorize/info`. Security-reviewed (clean). **Also shipped:** sftp subsystem (bridged to in-container `sftp-server`) and the framework backend-adapter doc ([`framework-backends.md`](framework-backends.md) — Hermes/Goose/Remote-SSH point at the SSH endpoint). **Remaining:** live VS Code Remote-SSH verification on a real island (operator queue above). (near-done)
- [ ] **Agent-IDE integration** — let VS Code / Cursor / Zed / VSCodium open an island as a remote-dev target (the editors discussed on the Pages site). *Now:* "Attach to Running Container" via a remote Docker context (`DOCKER_HOST=ssh://…`) works today, no Dejima change. *Phase 3:* the SSH-façade makes "Remote-SSH into an island" universal across forks (plain SSH — no proprietary-extension licensing that bites VS Code forks). *Later (figure out):* a **native Dejima editor extension** — browse islands/agents, attach, see agent-state, and run `port` trades from the IDE instead of generic remote-SSH. (open design)
- [x] **Phase 4 — read-write trading** — `:rw` grants + `dejima port write` (island → scope), symlink-safe (`resolveWriteTarget` blocks `..`/symlink escapes), fail-closed `trade.write` ledger. Live-Docker regression in `integration.sh`. Use with care — it writes the user's primary files.
- [ ] **Phase 5 — live brokered mount** — FUSE/9p/virtio: island sees a directory, broker mediates+logs each op; only after RW. (weeks)
- [ ] **Capability brokering** — open: broker a curated allowlist of host commands (Shortcuts/Notes) or hold the files-only line. Collapses ledger tractability; fenced. (open design)

**Known concerns (documented in spec/runbook; not blockers):**
- *docker-cp UID mapping* — host files keep their numeric UID (macOS ~501) + mode; agent is `dejima` UID 1000, so 0600 host files are EACCES until read-normalization lands.
- *macOS unix-socket limitation* — Docker Desktop/colima can't bind-mount the daemon socket into a container; drives the TCP-autonomy work above.

## Multi-agent — shipped (phases 0–7); follow-ups

- [ ] **TUI: seed multiple agents at create time** — parity with `init --agent X --agent Y`; today the TUI create flow picks one agent, then `a` adds more. UI-only. (hours)
- [x] **Scratch terminal in an island** — built-in `shell` agent type (`handlers.Shell`): a bash login shell on the island's `/workspace` (no isolated worktree), attachable. In the TUI add-agent picker as "Terminal" and via `dejima agent add X --type shell`. Glyphs reworked: `❯` terminal, `◆` AI agent, `■` headless. *(Future nicety: a transient `t`="open terminal here" that doesn't register an agent.)*
- [ ] **Cross-machine validation** — non-primary-agent attach + resize on Windows-client → macOS-daemon (historically fragile path); dogfood, not code.
- [ ] **Reassess agent naming / id scheme** *(low priority)* — `a1`/`a2` ids are the stable addressing handle (CLI `connect island/a2`, branch `agent/a2`, worktree `.agents/a2`, tmux `agent-a2`), while the label is optional/renamable. The TUI now leads with the label (falls back to type, id rides along muted). Open question whether the id scheme itself should change — e.g. human-friendly auto-names, or deriving the handle from the label when one's given. Design only; no urgency.

## v2 — heavier features

Substantial engineering. Defer until v1 dogfood proves the foundation.

- [ ] **Per-agent / per-island ACLs within a shared project** — when multiple islands share a workspace, define which agent can read/write which paths. Useful for delegated work streams ("frontend can write under /web, backend under /api, both read /shared"). Wrapper-product territory mostly; primitives may belong here. (open design, week+)
- [ ] **Trust-on-first-use for new clients** — unfamiliar attaches blocked until user approves via push notification on an already-trusted device. The 2FA-shaped feature. (week)
- [ ] **Token-based auth (single `owner` role)** — `dejima token create --label phone` issues a token; CLI/API consumers carry it via env or header. Doesn't replace Tailscale identity, complements it. Foundation for the wider roles model below. (week)
- [ ] **Three built-in roles + per-island scope** — `owner` / `operator` (lifecycle but no purge) / `viewer` (read + observe). A token can be limited to specific islands. Lets wrapper products (Scusi, etc.) hold a service token with bounded power. (2 weeks)
- [ ] **Explicit auth non-goals (won't build inside Dejima)** — multi-tenant user UIs, OAuth/SSO, per-verb fine-grained ACLs, time-windowed tokens. Those belong in wrapper apps. Dejima ships **3 roles + island scope** and stops; anything richer is the wrapper's job. *(Same pattern as Postgres roles + Supabase auth.)*
- [ ] **Audit ledger** — append-only `~/.dejima/ledger.jsonl` of every API request + lifecycle event; optional HMAC-signed entries; `dejima audit` query verb. (week)
- [ ] **Backup / restore** — `dejima backup <name>` and `dejima restore` with a configurable destination (local path, S3, Backblaze, rsync target). User-configurable. (week)
- [ ] **microVM backend** — Firecracker/Apple Virtualization framework as an isolation upgrade. Real per-island VM rather than shared kernel. (weeks)
- [ ] **MCP server brokering** — proxy host MCP servers into islands (or run them inside); declarative per-project. (weeks)
- [ ] **Multi-user / RBAC** — team scenario. Auth model, identity, per-user quotas, project ownership. (weeks)
- [ ] **Manage foreign containers (not just islands)** — extend the daemon from "manage the agents/containers Dejima provisioned" to "be the management layer for arbitrary agent containers already on the host" (adopt/observe/lifecycle containers Dejima didn't create). A real product swing toward Portainer/compose territory that strains the island/containment model; deferred deliberately. The committed direction is the **open-ended handler registry** instead: many first-class agent *types* (claude-code, codex, headless/SDK loops, openclaw, hermes, …) on islands Dejima owns, via a declarative handler descriptor rather than a Go change per agent. (open design, week+)
- [ ] **Nested containers inside an island (per-island DinD)** — distinct from managing foreign containers: let an agent spawn its *own* containers inside its island (test sandboxes, image builds). Dejima deliberately keeps **no visibility** into these — they live in the island's blast radius and tear down with it. Today an island has no Docker access at all. Enabling it has two doors: mounting the host docker socket (trivial but effectively host-root — a containment break, **rejected**) vs. rootless Docker-in-Docker confined to the island namespace (preserves containment; costs image/privilege plumbing + overhead). If we do it, only the rootless-DinD door. Parked — reconsider on real demand. (open design)
- [ ] **Cross-host CLI** — `dejima --host <name>` first-class; multi-host registry; remote orchestration. (week)
- [ ] **Optional natural-language control layer** — `dejima ask "spin up an island for the api repo and hibernate the idle ones"`, and/or a TUI command palette, that translates intent into existing API calls with confirmation before any mutating action. Reuses the user's *already-present* agent credentials (e.g. Claude) — **no bundled model weights**. Wrapper-adjacent; could ship as a thin opt-in in core or as a separate tool. (open design, week)
- [ ] **Web / PWA reference client** — xterm.js-based browser client for the session API. Mobile-friendly. (weeks; separate repo)
- [ ] **Lock-based session check-in/check-out** — for explicit handoff between devices instead of shared-tmux. Add iff real demand. (week)

---

## Open questions to investigate

- [ ] **Compatibility with "OpenClaw"** — name flagged for investigation. Not a project I recognize (as of knowledge cutoff Jan 2026). Once identified: assess whether it's a CLI agent (bundle as image variant), an editor extension (no direct Dejima relationship), a protocol (potential feature), or something else. Goes in v1.x or v2 depending on what it turns out to be.
- [ ] **Local content ingestion for content-digesting agents** — agents like OpenClaw exist to digest emails, documents, and other *local* content, not just a git repo. How does such an agent, sealed inside an island, reach that content? Options span: (a) it's beyond Dejima — the wrapper app feeds content in over the API / via `cp` (consistent with "orchestration is the wrapper's job"); (b) a declarative per-island host-content mount (ties to the v2 "configurable host mounts" idea, and strains containment); (c) a brokered content channel. Leaning (a), but the question is open — decide before bundling any content-digesting agent as a first-class handler.
- [x] **Native Windows client** — done. The CLI cross-compiles cleanly for `windows/amd64` and `windows/arm64`; the `creack/pty` import is daemon-only so it doesn't affect the client. The one Unix-ism (SIGWINCH for terminal resize) is now behind build tags — Unix uses SIGWINCH, Windows polls. `make client-binaries` produces all six client targets under `dist/`. *Remaining*: GitHub Releases workflow so Windows users don't have to cross-compile on a Mac/Linux host. (~2 hours of CI work.)

## v2+ — tier-2 integrations (separate repos)

These don't live in the core dejima repo. They consume the API.

- [ ] **`dejima-slack`** — drive an island from a Slack channel; presence and events stream back.
- [ ] **`dejima-telegram`** — same shape, Telegram Bot API.
- [ ] **`dejima-ntfy`** — first-class config integration for ntfy.sh push notifications (could ship as a doc rather than code).
- [ ] **Native macOS notification helper** — small daemon-companion app that forwards webhook events to macOS Notification Center.

---

## Explicitly out of scope (for now)

Things worth saying "no" to clearly so they don't keep coming up:

- **Inter-island communication channels** (a "trade" primitive, a message bus, RPC between islands). The whole point of islands is containment; any sanctioned cross-island channel is a context-bleed vector. Multi-agent orchestration belongs in wrapper apps that drive multiple islands via the public API — not inside Dejima.
- **Hosted/SaaS variant.** Dejima is OSS, self-hosted. No managed offering planned.
- **Windows host support** (running `dejimad` + Docker on Windows). The client works on Windows; the host doesn't. Out of scope.
- **Enterprise compliance certifications.** SOC 2 etc. are post-team-product, not v1/v2.
- **Built-in cost tracking for LLM API spend.** Out of scope; consume webhook events into your own dashboard.
- **Bundling LLM weights into Dejima itself.** The agents are the LLMs, and they live *inside* islands; a model baked into the daemon would bloat the binary, assume hardware (GPU/RAM), and go stale. Any natural-language / assistant features reuse credentials the user already has (see the optional NL control layer in v2) — Dejima ships no weights. Same principle as no-built-in-cost-tracking.
- **Real-time collaborative editing inside the workspace.** Sessions are shared-tmux, not shared-Cursor. Different problem.
