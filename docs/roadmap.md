# Dejima Roadmap

**Last updated:** 2026-06-18

This is the living roadmap for Dejima. Items are grouped by phase and sized roughly. Status legend: `[x]` = built, `[~]` = in progress, `[ ]` = pending.

**Phases ≠ versions.** The `v1` / `v1.x` / `v2` headings are *planning buckets*, not release numbers. Released builds follow semver: the **first public release is `v0.1.0`**, and we stay in **0.x** — where the CLI/API may still break and `api_version` may bump — until we deliberately commit to API stability at **`v1.0.0`** ("safe to build on"). `api_version` (an integer client/daemon contract) is tracked separately from the semver tag.

---

## 🎯 Critical path — what's next after 0.1.0 (priority order)

The forward priorities, distilled from the 2026 competitive review (see
`strategy/competitive-gap-assessment.md`). These cut across the phase buckets below.

1. **Audit log + read/export + viewer** (under v1.x) — the governance moat; decided 2026-06-19 to live in Dejima (a tamper-evident record needs engine-level placement).
2. **Team rung** — token auth + 3 roles + island scope + an activity feed. The solo→team conversion bridge; **promoted out of "v2, someday"** — it's the next major rung after the solo launch + audit. (Detailed items still filed under v2 auth; this is their priority home.)
3. **Audited MCP brokering** — table stakes (MCP is the default agent tool layer) *and* a differentiator (deny-by-default, ledgered). Pulled forward from v2.
4. **Language SDKs (Py/TS)** — **gated to API stability (v1.0).** Until then: example snippets + an OpenAPI spec so builders self-generate. Don't hand-maintain SDKs against a 0.x API.

Correctly deferred: microVM, multi-tenant SaaS, cross-host orchestration, in-Dejima agent orchestration.

---

## 🧑 Operator verification queue (built, needs a live run)

These shipped to `master` with unit/security review but can't be exercised from the
build island (no live Docker/macOS host here). Run them on Minion and feed findings back.

- [x] **OpenClaw idles in a Home Island, not crash-loops.** Verified on Minion 2026-06-15:
  `dejima home create --agent openclaw` self-installs openclaw (`2026.6.6`) and the gateway
  reaches `ready` and idles (no zombie, no restart-loop). `--allow-unconfigured` alone was
  *not* enough — inside a container OpenClaw defaults to `bind=auto` (0.0.0.0) and refuses
  to start without auth, so the launch also needs `--bind loopback` (`d20e5f9`). Also wired
  `home create --agent openclaw` to reuse the baked handler with `role=home` + `DEJIMA_HOME`
  (`f188031`; server home-role gate now keys on attachability, not the literal "headless"
  type). `internal/handlers/handlers.go`, `cmd/dejima/home.go`.
- [x] **#8 macOS TCP autonomy reachability** — verified on Minion 2026-06-15. From inside an
  island, `host.docker.internal:7274/v1/healthz` with the daemon-injected bearer token → 200;
  own-island routes → 200, another island's → 403 (scoping holds). Confirmed live in the
  OpenClaw Home Island test, so `DEJIMA_HOST=host.docker.internal:7274` is the correct
  autonomy dial on Minion's Docker. (`runbook-openclaw-home-island.md §5.2`)
- [x] **#9 SSH-façade live + VS Code Remote-SSH — verified on Minion↔GIZMO 2026-06-17.**
  Shell, sftp, and VS Code Remote-SSH all land in `/workspace`. Flushed out two real
  daemon bugs: (1) the no-PTY exec path set `c.Stdin = ch`, so `cmd.Wait` blocked until the
  SSH channel EOF'd — which an interactive client never sends — hanging exec and stalling VS
  Code at "checking for existing agent host"; fixed by closing the channel when the command
  exits, not on stdin EOF. (2) The façade rejected every non-`session` channel, so VS Code's
  dynamic port-forward (`direct-tcpip`) to its in-container server failed; added `direct-tcpip`
  bridging via `docker exec` + bash `/dev/tcp`. Onboarding is now one command per device
  (`dejima ssh enroll`: account-wide key + `~/.ssh/config` entries), and `c` / the printed
  `code --remote ssh-remote+dejima-<island> /workspace` one-liner open straight at the repo.

---

## 🛠️ Dogfood session 2026-06-17–18 (v0.1.11→v0.1.34)

A live Minion↔GIZMO (macOS host ↔ Windows client) session that took the
SSH-façade and self-update paths from "built" to "verified", plus a TUI overhaul
and the substrate-level fix for the OpenClaw OOM (#23).
All on `master`, unit-tested; the self-update surface was `/security-review`d clean.

- **SSH-façade end-to-end (#9/#14 verified):** exec-channel stdin-deadlock fix +
  `direct-tcpip` port forwarding (so VS Code/Cursor Remote-SSH connect and edit
  `/workspace`); see the operator-verification note above.
- **Frictionless onboarding:** `dejima ssh enroll` (account-wide key + `~/.ssh/config`
  entries, daemon writes its own `authorized_keys` so there's no user-vs-root mismatch);
  open-in-editor (`c` / `code --remote ssh-remote+dejima-<island> /workspace`).
- **TUI overhaul:** `⏎` opens in a new tab; `m` per-row action menu; `s` Settings
  (preferred editor · group-by-repo · connection target); configurable editor
  (VS Code/Cursor/Windsurf/Antigravity); tab titles (`dejima` / `<island>-<agent>`);
  decluttered footer.
- **Self-update made trustworthy (#18/#22):** the apply runs synchronously so real
  failures are reported (no more silent "updating…" no-op); preflights passwordless
  sudo for system installs; the daemon trusts `InstallMeta` for source-vs-release mode
  (a source build on a clean tag was misdetected as release and failed replacing its
  root-owned binary).
- **Supervised headless agents (#23 partial):** honest liveness (a self-restarting
  agent reads "running", not "died"), exponential backoff (2s→60s) replacing the flat
  3s respawn, and a visible `restarts: N` count (amber "crash-looping — likely OOM" at ≥3).
- **Per-island resource controls (#23):** OOM priority (stack-rank which island the
  kernel sacrifices first, via `--oom-score-adj`; create-time, so a change prompts
  "recreate to apply?") + an optional memory limit (`docker update --memory`, live),
  API-first (`PUT /v1/islands/{name}/resources`, surfaced in `GET /v1/islands/{name}`)
  with a TUI Resources overlay. Default is unlimited (overcommit) — no artificial cap.
- **Substrate VM-memory detection + fix (#23 root cause):** the real cause of the OOMs
  was the Docker VM itself — colima defaults to ~2 GB on a 24 GB host, and that VM is the
  pool *all* islands share, so no per-island knob helps. New `internal/vmmem` reads host
  RAM, recommends a size (¾·host, leaving the host ≥4 GiB), and judges undersizing;
  surfaced as `host/vm/vm_recommended_bytes` on `GET /v1/overview`, an amber TUI banner,
  a `dejima doctor --fix` that scripts the `colima stop && colima start --memory N` resize,
  and an onboard env-summary line.
- **Capability-brokering memo ratified** (2026-06-15) and built through the macOS
  Apple Shortcuts adapter.
- **#23 now addressed end-to-end** — prevented (right-sized VM), survivable (per-island
  memory limits + OOM priority), self-healing (backoff + restart count), and visible
  (TUI banner + doctor check). Remaining: a live Minion run to confirm the resized VM
  ends the OOMs.

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
- [x] All polish items shipped + unit-tested: warn on dangling identity ref · verify token on push · `handleGitHubRepos` handler test · `SetPrimaryID` unit test · Enterprise host in auth push · "N of M" repo-cap indicator · disambiguate duplicate-label rows.
- [x] **Live `git push` verified on Minion (2026-06-17)** — and it flushed out a real launch-blocker: the daemon materialized gh's *legacy* config into the read-only `/opt/host/gh-config` mount, so gh's first-use migration write failed → no credential helper → clone crash-looped the island. Fixed by emitting gh's already-migrated schema (`users:` map + `config.yml` version marker); regression test runs the real `gh auth setup-git` on a read-only dir.
- [x] **Commit authorship now derives from the identity** (#19): the push authenticated as the identity but authored commits with the host gitconfig's email (GitHub misattributes by email). Daemon materializes a per-island gitconfig (login + GitHub noreply email) over `/opt/host/gitconfig`; numeric id captured at `auth push` for the canonical `<id>+<login>@users.noreply` form. **Live-verify pending** (re-`auth push` to capture id; recreate identity islands).

### `feat/secure-island-routing` (merged `57ecb32`) — close the in-island control-plane hole
- Fixes a critical pre-existing containment hole: the operator unix socket was bind-mounted into every Linux island. Now the control socket is **not** mounted; islands reach the daemon only over the token-authenticated, island-scoped TCP path; `agent-event` moved onto it (authenticated, anti-spoof); `host.docker.internal:host-gateway` added. Docs: `docs/secure-island-routing.md`.
- [ ] Live-verify: in-island telemetry/autonomy over the token path on Minion (macOS).
- [ ] Open (**parked — no native-Linux host in the fleet**): native-Linux token-listener reachability (loopback bind unreachable via the bridge gateway). Repro + fix pre-scoped in `docs/launch-checklist.md` §L4 (bind the token listener to the bridge gateway); revisit if a Linux daemon host appears.

### `feat/host-terminals` (merged `d991bc4`) — operator host terminals
- Uncontained operator shells in tmux on the daemon host (humans, **not** agents — "agent ⇒ always a container"). `internal/hostterm` + `internal/bridge` host PTY; `/v1/terminals*` operator-only (`dejimad --host-terminals`, off by default, audited, island-token-denied by test); TUI "Host · not contained" section (`t` to create+attach). Docs: `docs/host-terminals.md`.
- [ ] Live-verify: interactive attach on a daemon with `--host-terminals` + tmux installed.
- [x] **`dejima term` CLI built** (#16, `afb436a`): `ls`/`new`/`attach`/`rm`/`relabel`, mirroring the TUI section. Thin client of the same gated API — the `--host-terminals` capability + operator-only auth (island tokens 403) apply identically, so it widens convenience, not the security boundary. (Reverses the earlier TUI-only deferral, which on review didn't actually move the boundary.)

---

## v1 (current — alpha, in daily use)

The v1 vertical slice. Buildable, testable, and in daily use — now in alpha. Rough-edge hardening ongoing.

- [x] M0 — Foundation: Go module, repo skeleton, CI on macOS + Linux
- [x] M1 — MVP island + daemon (CLI: `init`, `connect`, `ls`, `status`, `purge`; Unix-socket API; Dockerfile)
- [x] M2 — Lifecycle: `hibernate`, `wake`, `reset`; daemon adopts existing islands at startup
- [x] M3 — Multi-attach session via websocket API + presence
- [x] M4 — Service install (launchd/systemd); Tailscale-pinned TCP listener; webhooks; per-agent shims (Claude Code installed)
- [x] M5 — Resource caps; `exec` / `cp` / `logs` access verbs; multi-agent disambiguation
- [x] **Codex CLI as a bundled agent** — second agent shim, per-agent state volume mount, honest "agent-agnostic" claim
- [x] M6 — Dogfood on Mac mini for one week (met; in daily use). Rough-edge hardening ongoing.

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
- [ ] **Site: "Is Dejima right for you?" copy-paste prompt** — zero-backend widget on `index.html` that copies a crafted prompt (summarizes Dejima, references the site URL + `api.html` API docs, asks the visitor's situation, asks the model whether Dejima fits) to the clipboard for the visitor to paste into their *own* Claude/ChatGPT. No hosted inference, no bundled weights — honors the no-SaaS / no-weights non-goals. (hours)
- [ ] **Site messaging refresh** — landing copy lags shipped work. Surface: SSH-façade → VS Code/Cursor Remote-SSH on-ramp, per-daemon GitHub identities, trustworthy self-update, host terminals, capability brokering, `clone`, panic / unpushed-work guards. Strongest under-told narrative: *"turn a Mac mini into a personal agent server you edit in your real IDE."* Diff the recent commits against the site copy and propose edits before applying. (hours)
- [ ] **Submit to homebrew-core** — eventual `brew install dejima` without the tap prefix. Months of stewardship; defer until v1.x has users. (months)
- [ ] **`dejima update` epic** — one role-aware command that pulls, installs, and restarts (server + client). Consolidates the four former sub-items below.
  - **V1 (dual-mode, local)** — auto-detect: in a git checkout with Go → *source path* (`git pull` + `make install` + restart); else → *release path* (download the `GOOS/GOARCH` asset, checksum-verify against published `SHA256SUMS`, atomic self-replace via go-update/selfupdate — Windows = rename-aside swap since a running `.exe` can't be overwritten). **Role-aware:** client swaps `dejima` only; server swaps `dejima`+`dejimad` then `dejima service restart` (islands survive via `AdoptExisting`; only live attaches blink). **Flags:** `--check` (dry-run/availability), `--yes`, `--channel stable|edge`, `--notify` (fire a webhook before any daemon restart). Reuses release CI + `SHA256SUMS` + `service restart` + events. ~1–1.5d. **Prereq for the *client* half: a release cadence** (even auto-tagged `v0.x` edge builds from CI) — a binary-only client can't build from source.
  - *folds in:* **self-update** (client download+verify+swap → the release path) · **update-available check** (`--check` / daily-opt-in → `update.available` webhook) · **stable vs edge channels** (`--channel`).
  - **Deferred (v2): remote daemon update** — GIZMO → Minion daemon self-update over an *authenticated admin endpoint* (process-restart-behind-launchd + authz; ties to the per-island token work). Notify-then-apply, never silent.
- [x] **Container watchdog goroutine** — daemon polls `Status()`+`Inspect()` every 30s and emits `container.crashed` on unexpected exits (running→stopped/missing) and on restart-count climbs (flapping under `--restart unless-stopped`, e.g. repeated OOM kills); payload carries status/exit_code/oom_killed/restart_count/reason. Edge-triggered (no re-emit at steady state), primes silently on first scan, and stays quiet while panic mode stopped everything deliberately. (`internal/api/watchdog.go`)
- [x] **`dejima upgrade <name>`** — recreate a container against a fresher island image while preserving volumes (also `--all`, and the `u` key in the TUI). Pairs with **`dejima image build`**: the build context is embedded in `dejimad`, so the image rebuilds on the daemon host with no source checkout; missing images auto-build on first `dejima init`.
- [x] **`dejima panic`** — stop every island immediately; write a `~/.dejima/PANIC` flag preventing auto-restart until removed. **Shipped:** `dejima panic` (engage), `--clear` (remove flag + restart islands meant to be running), `--status`; daemon `GET/POST/DELETE /v1/panic`; `AdoptExisting` refuses to auto-start while the flag is set (survives a daemon restart); state surfaced in `/overview`, `doctor`, and a TUI alarm banner. Emits `daemon.panic-engaged`/`daemon.panic-cleared`.
- [x] **Unpushed-work guard on `purge` + `dejima uninstall`** — **guard:** `purge` / `DELETE /v1/islands` inspect `/workspace` (dirty + ahead-of-upstream) and refuse with 409 unless `--force`, naming the at-risk file/commit counts and branch; a non-running island can't be verified so it also requires `--force`. A blocked TUI purge offers a force-purge confirm. **uninstall:** `dejima uninstall` runs the whole clean-removal sequence — pre-flights the unpushed-work guard across *all* islands (so it never half-uninstalls), then guarded-purges every island → uninstalls the service → removes the dejima/dejimad binaries → deletes `~/.dejima`. Confirms first (unless `--yes`); `--force` bypasses the guard, `--keep-data` preserves `~/.dejima`; degrades gracefully on permission errors (suggests `sudo rm`).
- [x] **`dejima refresh-creds <name>`** — covered by `dejima upgrade <name>`: recreating the container re-assembles all credential mounts (and re-materializes the Claude seed) without touching the workspace. `dejima auth push` handles getting fresh Claude credentials onto the daemon host in the first place.
- [x] **`dejima clone <name> <new-name>` — copy an island (with its credentials)** — **shipped:** `POST /v1/islands/{name}/clone` + `dejima clone` duplicate an island: new config + byte-for-byte copies of its workspace **and** home volumes via `runtime.CopyVolumeData` (throwaway container, `cp -a`, source mounted read-only). Volumes are populated before the container starts so `start.sh` sees `/workspace/.git` and skips re-cloning. Owner/tags/agents carry over; **Title and Port grants are deliberately dropped** (clone shows its own name and starts deny-all — never silently inherits host access). Caveats below stand (host-bound tokens, duplicated `~/.claude` state). Original scope note retained: Because all creds / permissions / tool-auth live in the per-island `/home/dejima` home volume ([`multi-agent-spec.md`](multi-agent-spec.md) §6), cloning carries them along for free — so this is the natural "copy an island with everything" primitive, and it also underpins a *true* island rename (clone to a new name + delete the old; today rename is a cosmetic display **title**, since Name is immutable infra identity). Caveats: device/host-bound tokens may not survive a cross-host clone, and duplicating `~/.claude` duplicates session/runtime state (the §6 shared-home note). **Copying *just* credentials/permissions to another island is deliberately out** — it reintroduces the per-tool-token-path enumeration the whole-home volume was chosen to avoid; if ever needed, extend the `dejima auth push` seeding per-tool instead of doing volume surgery. (1-2 days)
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
- [ ] **Audit log + read/export + viewer — the governance moat (pulled forward from v2).** The tamper-evident *Port-crossing* ledger is already shipped (hash-chained, host-side). This extends it to an **operational** audit log (`~/.dejima/ledger.jsonl`: API requests + lifecycle events, opt-in, optional HMAC) **and adds a read/export API + a basic viewer** — not just `dejima audit --verify`. **Decided 2026-06-19 that audit lives in Dejima, not the wrapper:** a tamper-evident record can't be delegated to a webhook-fed layer (engine-level placement required), which is why the crossing-ledger is already here. Compliance dashboards / multi-org rollups / retention-as-product stay above. This is the regulated-team wedge and is promised on the site's Teams page. (week+)
- [ ] **Opt-in trust-on-first-use** for new clients — paranoid mode for users who want stronger-than-tailnet auth. Off by default. (week)
- [ ] **Opt-in egress allow-list per island** — `network.allow = ["api.anthropic.com", ...]` in project config. Default: open. (day)

---

### Observability — real-time signals for dashboards / wrapper tooling

Dejima's stance: surface rich real-time state via the API and let wrapper tooling own history, aggregation, and per-user/per-org rollups (same division as the built-in-cost-tracking non-goal below). These are the real-time signals still worth exposing.

- [x] **Crash health in island detail** — `oom_killed`, `restart_count`, `exit_code` from `docker inspect`, surfaced on `GET /v1/islands/:name` and in the TUI detail pane. Signals an agent killed by its memory cap or a flapping container — facts a remote client can't observe itself. (done)
- [x] **Per-island disk usage** — workspace + home volume sizes surfaced alongside mem/cpu in `status` and the TUI detail pane. `runtime.VolumeSizes` does one `docker system df -v` call mapped by volume name; the daemon caches it 30s (slower than `docker stats`, disk drifts slowly) and only populates it on the detail endpoint (`IslandInfo.Disk`), never the list. Size reads 0 on storage drivers that don't report it (rendered only when > 0). (`internal/runtime/docker.go`, `internal/api`)
- [x] **Prometheus `/metrics` endpoint** — `GET /metrics` exposes islands-by-state, per-island cpu/mem/disk (workspace+home), restart/OOM counts, attached clients, panic state, and daemon build info, in Prometheus text-exposition format. Per-island series carry `island` + `owner` labels (per-team rollups via the ownership work). Hand-rolled (no client_golang dep, matching the docker-CLI ethos); reuses the cached stats/disk samples. Operator-level — the token-auth path default-denies it, so an island can't scrape the fleet. *(agent-idle seconds deferred — needs the agent-liveness heartbeat path.)* (`internal/api/metrics.go`)
- [x] **Agent-process liveness** — distinguishes a crashed *agent* inside a still-running container from a healthy one via the tmux pane command (the cheap path; no shim changes). `agentLiveness` adds an `"exited"` agent state — tmux session alive but its foreground fell back to a bare shell (agent process died while `start.sh` keeps the container up) — alongside `running`/`stopped`. Never fires for the shell agent type (a prompt is its healthy state). Surfaced in `IslandInfo.Agents[].State`, `dejima status`, a red TUI glyph + detail line, and a `doctor` WARN row. Detail-endpoint only. Complements the container watchdog, which only sees container exits. *(Headless/SDK agents — which have no tmux — still need the heartbeat-shim path; tracked under Observability "agent-process liveness" follow-up.)*
- [x] **Island ownership + tags** — `dejima init --owner <label> --tag k=v` (repeatable) persists a creator label (default `<user>@<host>`) and free-form tags on the island (`project.Project.Owner`/`Tags`, toml). Surfaced in `IslandInfo`, `dejima status`, and the TUI detail pane. Informational only (no auth model yet); enables per-user/per-team rollups in wrapper dashboards and pairs with the token/roles work in v2. Tags are sanitized server-side (empty keys dropped). (`internal/project`, `internal/api`, `cmd/dejima`)
- [~] **Doctor: daemon supervision check** — doctor now reports *how* dejimad runs, not just whether it's reachable. `service.Detect()` (`internal/service/detect.go`) classifies the supervision mode — launchd system/gui/user domain, systemd --user (with enabled + linger), or none — and `doctor` WARNs on the reboot-survival footguns: an **orphan** (reachable but unsupervised, hand-run), a **user-domain LaunchAgent** or **linger-off systemd unit** that won't return after reboot on a headless box, and a **system plist present but not loaded**. `dejima service status` prints the same supervision line + concern. Pure classifiers are unit-tested. **Remaining:** the "plist loaded but a *different* daemon answers" case (orphan holding :7273 while the service crash-loops) — needs comparing the answering daemon's pid/version to the supervised one; deferred. (hours)
- [x] **`daemon.started` webhook event** — emitted on dejimad startup with `{version, api_version, listen:[...]}` in the payload. On a headless host this is the only push-shaped way to learn the box rebooted or the daemon crashed and was restarted by its supervisor; pairs with the container watchdog's `container.crashed`. (`Server.EmitDaemonStarted`, fired from `cmd/dejimad`)

---

## v1.x — open design questions

Questions worth answering before committing to an implementation.

- [ ] **Shared workspace volume across islands** — `dejima init --workspace shared:foo` joins an existing workspace volume instead of creating a fresh one. Enables "multiple role-based agents on the same code, each in its own island" without forcing git-roundtrips between them. Open: how to handle merge conflicts when N agents write to the same files; whether agent state stays per-island (yes) or shared (probably no). (open design, 1-2 days when settled)
- [x] **Multi-island sibling view in TUI / `dejima ls`** — islands sharing a repo can read as one project. **CLI:** `dejima ls -g`/`--group` groups them under a per-repo header with an island count. **TUI:** `p` toggles a grouped view — islands are reordered so siblings are contiguous and a muted `◇ <repo>` header precedes each group (injected like the Host header, so the cursor mapping is untouched; the cursor re-anchors on its island across the toggle). Pure helpers (`groupByRepo`, `orderedIslands`) unit-tested.
- [ ] **Agent-scoped file access** — once islands host N agents on per-agent git worktrees, the file verbs (`GET/PUT …/files`, `dejima cp`, `dejima exec`) become path-ambiguous. The multi-agent MVP just defaults them to the primary worktree (`/workspace`, no new surface). Open question for later: a richer per-agent file surface — `--agent` targeting on `cp`/`exec`/`files`, and possibly a browse/read API over an agent's worktree. (open design, when multi-agent lands)
- [~] **Agent LLM provider keys + first-class adapters (shipped substrate; follow-ups open).** v0.1.35 shipped the substrate end-to-end (branch `feat/agent-provider-keys`): `internal/providercreds` account-wide key store → read-only `/opt/host/llm` per-island mount (key never an env var) → OpenClaw `init.sh` shim → proactive `missing-provider-auth` health; API (`/v1/credentials/providers`, `…/agents/{id}/config`, `/v1/agent-types`), CLI (`dejima provider`, `dejima agent config/types/open`), TUI overlay (`v`) + missing-key row flag, and `dejima agent open` channel forward. Also hardened purge against a wedged-container freeze. **Open follow-ups:**
  - [ ] **Letta** first-class adapter — `letta server`, env `OPENAI/ANTHROPIC_API_KEY`, REST/UI on **8283** (`agent open` works). Needs a live in-island install+launch check.
  - [ ] **Hermes** first-class adapter — `hermes gateway`, env/`hermes auth add`; gateway is a **messaging bridge** (no localhost UI) → `GatewayPort` 0, injection-only. Live check.
  - [ ] **Goose** first-class adapter (pending confirm) — `goosed`, env `GOOSE_PROVIDER`/`GOOSE_MODEL`, web UI on **3000**. Live check.
  - [ ] **Settings → Provider keys** TUI pane (account-wide key manager) — CLI `dejima provider` + the per-agent overlay's inline entry cover it today.
  - [ ] **TUI "open gateway" action** — the CLI `dejima agent open` covers it; a quit-to-run TUI affordance would round it out.
  - [ ] **Live verify on Minion** — recreate an OpenClaw island via the guided flow, set the Anthropic key, confirm a task runs (no ProviderAuthError).
- [ ] **Agent-orchestration layer (PARKED — deliberately out of the substrate).** The 2026-06-18 LLM-provider work shipped the *substrate* pieces — provider-key injection, per-agent provider/model, proactive missing-key health, `dejima agent open` channels (see `agent-adapters.md`, `internal/providercreds`). It deliberately stopped short of an *orchestration* layer: a generic provider/model catalog, model routing/fallback, per-agent model-selection-as-product, usage/cost dashboards, an in-dejima LLM-ops console. Per the containment thesis ("multi-agent orchestration belongs in wrapper apps that drive islands via the public API"), those belong on top of the API, not inside dejima. Reconsider only if a concrete need can't be met by a wrapper app over the existing endpoints (`/v1/credentials/providers`, `/v1/islands/{n}/agents/{id}/config`, `/v1/agent-types`). The substrate exposes the data; the console is someone else's product. (revisit on demand)

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
- [~] **Capability brokering** — **decided 2026-06-15** ([`capability-brokering.md`](capability-brokering.md), spec [`capability-broker-spec.md`](capability-broker-spec.md)): fast-track a **narrow typed broker (Option C) now** — function-calling brains (OpenClaw/Hermes/Letta) need structured tool calls, not just files. `POST /v1/capabilities/execute` (named target + string→string args) → per-platform adapters: **macOS Apple Shortcuts**, **Linux `~/.dejima/capabilities/` scripts**. Deny-all per-island grants (`dejima cap grant/revoke/ls`), no shell, fixed-schema `capability.*` ledger. **General command broker (B) permanently rejected** (ledger-intractable). Phasing in the spec §9. **Shipped: phases 1–3** — per-island grants + `dejima cap grant/revoke/ls` + `capability.*` ledger (`internal/project/capabilities.go`, `internal/api/capability.go`); the `script` adapter + adapter registry (`internal/capability`, trust gates + bounded exec + timeout); and the token-reachable `POST /v1/capabilities/execute` (`accessTokenOwn`, deny-all grant check, ledgered). Linux works end-to-end. **Pending: phase 4** — the macOS Apple Shortcuts adapter (operator-verification item below).

**Known concerns (documented in spec/runbook; not blockers):**
- *docker-cp UID mapping* — host files keep their numeric UID (macOS ~501) + mode; agent is `dejima` UID 1000, so 0600 host files are EACCES until read-normalization lands.
- *macOS unix-socket limitation* — Docker Desktop/colima can't bind-mount the daemon socket into a container; drives the TCP-autonomy work above.

## Multi-agent — shipped (phases 0–7); follow-ups

- [ ] **TUI: seed multiple agents at create time** — parity with `init --agent X --agent Y`; today the TUI create flow picks one agent, then `a` adds more. UI-only. (hours)
- [x] **Scratch terminal in an island** — built-in `shell` agent type (`handlers.Shell`): a bash login shell on the island's `/workspace` (no isolated worktree), attachable. In the TUI add-agent picker as "Terminal" and via `dejima agent add X --type shell`. Glyphs reworked: `❯` terminal, `◆` AI agent, `■` headless. *(Future nicety: a transient `t`="open terminal here" that doesn't register an agent.)*
- [ ] **Cross-machine validation** — non-primary-agent attach + resize on Windows-client → macOS-daemon (historically fragile path); dogfood, not code.
- [ ] **Reassess agent naming / id scheme** *(low priority)* — `a1`/`a2` ids are the stable addressing handle (CLI `connect island/a2`, branch `agent/a2`, worktree `.agents/a2`, tmux `agent-a2`), while the label is optional/renamable. The TUI now leads with the label (falls back to type, id rides along muted). Open question whether the id scheme itself should change — e.g. human-friendly auto-names, or deriving the handle from the label when one's given. Design only; no urgency.
- [ ] **TUI: create/add launches in a new tab + manual-name tab titles** — the creator (`n`) and add-agent (`+`) flows finish by attaching inline (the dashboard window is taken over); they should open the new island/agent in a **new terminal tab** instead (reuse `openAgentWindow`; graceful fallback to inline attach when not in tmux/macOS/Windows), leaving the dashboard up — same behavior `o`/`⏎` already use. Pair with: window-tab titles use the **manually-set** names (island `Title`→`Name`, agent `Label`→`ID`) instead of `<island>-<agentID>`; the internal tmux session handle `agent-<id>` is left unchanged (stable addressing). (`cmd/dejima/tui_window.go`, `tui_create.go`, `tui_agentpick.go`; hours)

## v2 — heavier features

Substantial engineering. Defer until v1 dogfood proves the foundation.

- [ ] **Per-agent / per-island ACLs within a shared project** — when multiple islands share a workspace, define which agent can read/write which paths. Useful for delegated work streams ("frontend can write under /web, backend under /api, both read /shared"). Wrapper-product territory mostly; primitives may belong here. (open design, week+)
- [ ] **Trust-on-first-use for new clients** — unfamiliar attaches blocked until user approves via push notification on an already-trusted device. The 2FA-shaped feature. (week)
- [ ] **Token-based auth (single `owner` role)** — *(pull forward — the solo→team conversion bridge; with the 3-roles item below, this is the next rung after the solo launch).* `dejima token create --label phone` issues a token; CLI/API consumers carry it via env or header. Doesn't replace Tailscale identity, complements it. Foundation for the wider roles model below. (week)
- [ ] **Three built-in roles + per-island scope** — `owner` / `operator` (lifecycle but no purge) / `viewer` (read + observe). A token can be limited to specific islands. Lets wrapper products (Scusi, etc.) hold a service token with bounded power. (2 weeks)
- [ ] **Explicit auth non-goals (won't build inside Dejima)** — multi-tenant user UIs, OAuth/SSO, per-verb fine-grained ACLs, time-windowed tokens. Those belong in wrapper apps. Dejima ships **3 roles + island scope** and stops; anything richer is the wrapper's job. *(Same pattern as Postgres roles + Supabase auth.)*
- [ ] **(moved)** Operational audit ledger — consolidated into "Audit log + read/export + viewer" under v1.x (pulled forward; see above). It's the moat, so it's no longer a v2 deferral.
- [ ] **Backup / restore** — `dejima backup <name>` and `dejima restore` with a configurable destination (local path, S3, Backblaze, rsync target). User-configurable. (week)
- [ ] **microVM backend** — Firecracker/Apple Virtualization framework as an isolation upgrade. Real per-island VM rather than shared kernel. (weeks)
- [ ] **Audited MCP brokering (pull forward — table stakes).** Deny-by-default grants of specific MCP (Model Context Protocol) servers into an island, declarative per-project, with **every call ledgered** — the Port/file-broker pattern applied to tools. MCP is now the default agent tool layer (Anthropic CMA and most platforms connect to it), so this is *parity* and a *differentiator* (nobody audits MCP access). No longer a v2 nicety — treat as near-term. (weeks)
- [ ] **Language SDKs (Python / TS) — gated to API stability.** Thin clients over the *existing* HTTP/WS API; they add ergonomics, not capability (the hard part they hide is the WebSocket PTY session stream + reconnection). The CLI is already a Go client. **Don't ship/maintain SDKs against a still-breaking 0.x API** — until `v1.0` ("safe to build on"), provide copy-paste Py/TS snippets in the API docs + an OpenAPI spec for self-generated clients. Real SDKs land with `v1.0`. (week+ each; deferred to API stability)
- [ ] **Multi-user / RBAC** — team scenario. Auth model, identity, per-user quotas, project ownership. (weeks)
- [ ] **Manage foreign containers (not just islands)** — extend the daemon from "manage the agents/containers Dejima provisioned" to "be the management layer for arbitrary agent containers already on the host" (adopt/observe/lifecycle containers Dejima didn't create). A real product swing toward Portainer/compose territory that strains the island/containment model; deferred deliberately. The committed direction is the **open-ended handler registry** instead: many first-class agent *types* (claude-code, codex, headless/SDK loops, openclaw, hermes, …) on islands Dejima owns, via a declarative handler descriptor rather than a Go change per agent. (open design, week+)
- [ ] **Nested containers inside an island (per-island DinD)** — distinct from managing foreign containers: let an agent spawn its *own* containers inside its island (test sandboxes, image builds). Dejima deliberately keeps **no visibility** into these — they live in the island's blast radius and tear down with it. Today an island has no Docker access at all. Enabling it has two doors: mounting the host docker socket (trivial but effectively host-root — a containment break, **rejected**) vs. rootless Docker-in-Docker confined to the island namespace (preserves containment; costs image/privilege plumbing + overhead). If we do it, only the rootless-DinD door. Parked — reconsider on real demand. (open design)
- [ ] **Cross-host CLI** — `dejima --host <name>` first-class; multi-host registry; remote orchestration. (week)
- [ ] **Optional natural-language control layer** — `dejima ask "spin up an island for the api repo and hibernate the idle ones"`, and/or a TUI command palette, that translates intent into existing API calls with confirmation before any mutating action. Reuses the user's *already-present* agent credentials (e.g. Claude) — **no bundled model weights**. Wrapper-adjacent; could ship as a thin opt-in in core or as a separate tool. (open design, week)
- [ ] **Lightweight in-product help chat** — a small, ideally open-source assistant for "how do I…" questions about Dejima itself (distinct from the control layer above — answers, doesn't act). Constraints: the creds Dejima stores via `dejima auth push` are **Claude-Code-scoped** (not a free API key), so they can't simply back a separate chat endpoint; and **no bundled weights** (non-goal). On-brand option worth weighing: *"help is a Home Island"* — `dejima home create` an agent with the docs/roadmap mounted and attach to it, dogfooding the product instead of adding a new model path. (open design)
- [ ] **Web / PWA reference client** — xterm.js-based browser client for the session API. Mobile-friendly. (weeks; separate repo)
- [ ] **Lock-based session check-in/check-out** — for explicit handoff between devices instead of shared-tmux. Add iff real demand. (week)

---

## Open questions to investigate

- [x] **OpenClaw** — resolved: it's a first-class bundled agent (handler + Home Island, verified on Minion 2026-06-15). *(was: "name flagged for investigation — not a project I recognize.")*
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
