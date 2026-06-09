# Dejima Roadmap

**Last updated:** 2026-06-09

This is the living roadmap for Dejima. Items are grouped by phase and sized roughly. Status legend: `[x]` = built, `[~]` = in progress, `[ ]` = pending.

**Phases ≠ versions.** The `v1` / `v1.x` / `v2` headings are *planning buckets*, not release numbers. Released builds follow semver: the **first public release is `v0.1.0`**, and we stay in **0.x** — where the CLI/API may still break and `api_version` may bump — until we deliberately commit to API stability at **`v1.0.0`** ("safe to build on"). `api_version` (an integer client/daemon contract) is tracked separately from the semver tag.

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
- [ ] **`dejima self-update`** — client downloads the latest release asset, verifies its checksum, and swaps its own binary in place (sudo for `/usr/local/bin`). Fast-follow right after 0.1.0 so the *second* release reaches users easily. (half day)
- [ ] **Update-available check** — daily, opt-in, cached: compare the local semver to the latest GitHub release and surface a quiet hint in `doctor`/TUI; optionally emit an `update.available` webhook event so wrappers (Dispatch/ntfy) can notify. No telemetry, never auto-applies. (half day)
- [ ] **Stable vs edge channels** — released tags = stable; HEAD source build = edge. Today's Homebrew formula is fragile HEAD-only; split once Releases exist. (half day)
- [ ] **Daemon self-upgrade / restart** — the hard one: update a running `dejimad` (download new binary, stop, swap, restart), including the remote case (SSH to the host or a signed self-update endpoint) and the headless-launchd reload. Notify-then-apply, never silent. (open design, days)
- [ ] **Container watchdog goroutine** — daemon polls `Status()` every 30s; emits `container.crashed` on unexpected exits. (day)
- [ ] **`dejima upgrade <name>`** — recreate a container against a fresher island image while preserving volumes. (day)
- [ ] **`dejima panic`** — stop every island immediately; write a `~/.dejima/PANIC` flag preventing auto-restart until removed. (day)
- [ ] **`dejima refresh-creds <name>`** — re-mount host credentials without touching workspace (for when GitHub / Claude tokens rotate). (day)
- [ ] **Secrets at rest via Keychain / Secret Service** — webhook HMAC secrets and any future dejima-held tokens out of plaintext config. (day)
- [ ] **Idle auto-hibernate** — opt-in threshold (e.g., "hibernate after N hours with no client + no agent process"). (day)
- [ ] **`dejima onboard --provision-host`** — Mac-mini-as-home-server provisioning wizard. Walks through Energy Saver / Sharing / Homebrew / Tailscale / Docker / SSH config / `.zshenv` (auto-doing what it can, instructing for GUI-only steps), then hands off to the existing Dejima onboarding. Closes the "I just unboxed a Mac mini, what now?" gap. **Strategically important: shifts positioning toward "the easy way to turn a Mac mini into a personal AI agent server."** Full plan: [`docs/host-provisioning-plan.md`](host-provisioning-plan.md). (~1 week)
- [ ] **Adaptive first-run prompt** — Detect server vs client context (Docker + dejimad binary present? DEJIMA_HOST set? daemon reachable?) and ask a context-specific y/n/N question instead of the generic "first time?" — e.g., on a server: *"Need help setting this up for remote agent access?"*; on a client: *"Need help connecting to your Dejima host?"*. Same marker / never semantics; smarter framing. (half day)
- [ ] **Connection-failure offer** — When the CLI hits a "daemon unreachable" error for the first time on a host that has `DEJIMA_HOST` set, surface a one-shot *"Want help troubleshooting the connection?"* prompt. Doesn't fire on subsequent transient failures (one offer per session). (half day)
- [ ] **Webhook security hardening** — the URL itself is a secret today. Improvements: (a) require a strong HMAC secret by default rather than as opt-in, (b) optional bearer-token auth on the receiver, (c) generate a high-entropy ntfy topic suffix automatically when user types a bare topic name, (d) interactive secret prompt during `dejima service install`. Already partially shipped (HMAC + interactive secret prompt); the rest is roadmap. (1-2 days)
- [ ] **Headless-Mac service install via LaunchDaemon** — when there's no GUI session, LaunchAgents can't load. Detect this case and offer a sudo-elevated LaunchDaemon install (`/Library/LaunchDaemons/dev.dejima.dejimad.plist` running as `aoos`) as the persistent alternative. Today the fallback is starting `dejimad` via `nohup` which doesn't survive reboots. (1 day)
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
- [ ] **Per-island disk usage** — workspace-volume size surfaced alongside mem/cpu in `status`/TUI. Via `docker system df -v` (one call, mapped by volume name); caveat: size reads `N/A` on some storage drivers and the call is slower than `docker stats`, so poll it less often. The signal most likely to silently bite (clones + builds fill volumes). (half day)
- [ ] **Prometheus `/metrics` endpoint** — islands-by-state, per-island cpu/mem/disk, agent-idle seconds, restart/OOM counts. Makes Dejima ping-able by any org dashboard (Grafana) with zero bespoke UI, and is the natural fan-in point when many daemons report to one place. Strongest single enabler for the multi-employee high-level dashboard. (1-2 days)
- [ ] **Agent-process liveness** — distinguish a crashed *agent* inside a still-running container (`start.sh`'s `tail -f` keeps the container up even if the agent dies) from a healthy one. Cheap-but-pollish by checking the tmux pane command, or via a heartbeat from the agent shim. Complements the container watchdog, which only sees container exits. (1 day)
- [ ] **Island ownership + tags** — persist a creator label and free-form tags (`--tag team=web`) on each island; surface in `IslandInfo`. Enables per-user / per-team rollups in wrapper dashboards. Storable now even before the auth model lands; pairs with the token/roles work in v2. (half day)

---

## v1.x — open design questions

Questions worth answering before committing to an implementation.

- [ ] **Shared workspace volume across islands** — `dejima init --workspace shared:foo` joins an existing workspace volume instead of creating a fresh one. Enables "multiple role-based agents on the same code, each in its own island" without forcing git-roundtrips between them. Open: how to handle merge conflicts when N agents write to the same files; whether agent state stays per-island (yes) or shared (probably no). (open design, 1-2 days when settled)
- [ ] **Multi-island sibling view in TUI / `dejima ls`** — group islands that share a repo (or a workspace volume) visually so multi-agent setups read as one project with N agents. UI-only change. (hours)

## v2 — heavier features

Substantial engineering. Defer until v1 dogfood proves the foundation.

- [ ] **Per-agent / per-island ACLs within a shared project** — when multiple islands share a workspace, define which agent can read/write which paths. Useful for delegated work streams ("frontend can write under /web, backend under /api, both read /shared"). Wrapper-product territory mostly; primitives may belong here. (open design, week+)
- [ ] **Trust-on-first-use for new clients** — unfamiliar attaches blocked until user approves via push notification on an already-trusted device. The 2FA-shaped feature. (week)
- [ ] **Token-based auth (single `owner` role)** — `dejima token create --label phone` issues a token; CLI/API consumers carry it via env or header. Doesn't replace Tailscale identity, complements it. Foundation for the wider roles model below. (week)
- [ ] **Three built-in roles + per-island scope** — `owner` / `operator` (lifecycle but no purge) / `viewer` (read + observe). A token can be limited to specific islands. Lets wrapper products (Dispatch, etc.) hold a service token with bounded power. (2 weeks)
- [ ] **Explicit auth non-goals (won't build inside Dejima)** — multi-tenant user UIs, OAuth/SSO, per-verb fine-grained ACLs, time-windowed tokens. Those belong in wrapper apps. Dejima ships **3 roles + island scope** and stops; anything richer is the wrapper's job. *(Same pattern as Postgres roles + Supabase auth.)*
- [ ] **Audit ledger** — append-only `~/.dejima/ledger.jsonl` of every API request + lifecycle event; optional HMAC-signed entries; `dejima audit` query verb. (week)
- [ ] **Backup / restore** — `dejima backup <name>` and `dejima restore` with a configurable destination (local path, S3, Backblaze, rsync target). User-configurable. (week)
- [ ] **microVM backend** — Firecracker/Apple Virtualization framework as an isolation upgrade. Real per-island VM rather than shared kernel. (weeks)
- [ ] **MCP server brokering** — proxy host MCP servers into islands (or run them inside); declarative per-project. (weeks)
- [ ] **Multi-user / RBAC** — team scenario. Auth model, identity, per-user quotas, project ownership. (weeks)
- [ ] **Cross-host CLI** — `dejima --host <name>` first-class; multi-host registry; remote orchestration. (week)
- [ ] **Optional natural-language control layer** — `dejima ask "spin up an island for the api repo and hibernate the idle ones"`, and/or a TUI command palette, that translates intent into existing API calls with confirmation before any mutating action. Reuses the user's *already-present* agent credentials (e.g. Claude) — **no bundled model weights**. Wrapper-adjacent; could ship as a thin opt-in in core or as a separate tool. (open design, week)
- [ ] **Web / PWA reference client** — xterm.js-based browser client for the session API. Mobile-friendly. (weeks; separate repo)
- [ ] **Lock-based session check-in/check-out** — for explicit handoff between devices instead of shared-tmux. Add iff real demand. (week)

---

## Open questions to investigate

- [ ] **Compatibility with "OpenClaw"** — name flagged for investigation. Not a project I recognize (as of knowledge cutoff Jan 2026). Once identified: assess whether it's a CLI agent (bundle as image variant), an editor extension (no direct Dejima relationship), a protocol (potential feature), or something else. Goes in v1.x or v2 depending on what it turns out to be.
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
