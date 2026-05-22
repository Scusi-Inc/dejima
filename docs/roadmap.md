# Dejima Roadmap

**Last updated:** 2026-05-21

This is the living roadmap for Dejima. Items are grouped by phase and sized roughly. Status legend: `[x]` = built, `[~]` = in progress, `[ ]` = pending.

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

## v1.x — hardening (the easy wins)

Targeted fixes and quality-of-life additions surfaced during planning. Sized in hours unless noted.

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
- [ ] **🧑 USER TASK — Push the repo to `github.com/aoos/dejima`** so the install URLs actually resolve. (5 min)
- [ ] **🧑 USER TASK — Enable GitHub Pages from `master:/(root)`** so `aoos.github.io/dejima/install.sh` works. See [`docs/distribution.md`](distribution.md). (5 min, free)
- [ ] **🧑 USER TASK — Create `aoos/homebrew-dejima` repo + copy `homebrew/dejima.rb` to `Formula/dejima.rb`**. (1 hour, free)
- [ ] **🧑 USER TASK — Pick + register a custom short domain** (e.g. `dejima.sh`, `dejima.app`). Optional polish on top of GitHub Pages. (~$10-45/yr, 30 min)
- [ ] **GitHub Releases with prebuilt binaries** — tag-driven CI builds darwin-arm64/amd64 and linux-arm64/amd64 archives, attaches to releases. Unlocks fast `brew install` and Go-free curl installs. (hours)
- [ ] **Submit to homebrew-core** — eventual `brew install dejima` without the tap prefix. Months of stewardship; defer until v1.x has users. (months)
- [ ] **Container watchdog goroutine** — daemon polls `Status()` every 30s; emits `container.crashed` on unexpected exits. (day)
- [ ] **`dejima upgrade <name>`** — recreate a container against a fresher island image while preserving volumes. (day)
- [ ] **`dejima panic`** — stop every island immediately; write a `~/.dejima/PANIC` flag preventing auto-restart until removed. (day)
- [ ] **`dejima refresh-creds <name>`** — re-mount host credentials without touching workspace (for when GitHub / Claude tokens rotate). (day)
- [ ] **Secrets at rest via Keychain / Secret Service** — webhook HMAC secrets and any future dejima-held tokens out of plaintext config. (day)
- [ ] **Idle auto-hibernate** — opt-in threshold (e.g., "hibernate after N hours with no client + no agent process"). (day)
- [ ] **Interactive TUI (`dejima` with no args)** — bubbletea-based dashboard for browse/manage/dive: live state, presence, keyboard nav, single-key actions (connect / hibernate / wake / reset). One-shot CLI verbs still work for scripts. (1 day)
- [ ] **Default-on attach notifications at install** — `dejima service install --notify <url>` becomes the recommended path; first install prompts for a webhook URL. Awareness without surveillance. (hour)
- [ ] **Opt-in audit log (`Ledger`)** — append-only `~/.dejima/ledger.jsonl` of operational events. **Off by default**, never silently enabled, easy to disable. For compliance contexts only. (week)
- [ ] **Opt-in trust-on-first-use** for new clients — paranoid mode for users who want stronger-than-tailnet auth. Off by default. (week)
- [ ] **Opt-in egress allow-list per island** — `network.allow = ["api.anthropic.com", ...]` in project config. Default: open. (day)

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
- [ ] **Web / PWA reference client** — xterm.js-based browser client for the session API. Mobile-friendly. (weeks; separate repo)
- [ ] **Lock-based session check-in/check-out** — for explicit handoff between devices instead of shared-tmux. Add iff real demand. (week)

---

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
- **Windows host support.** Linux + macOS only.
- **Enterprise compliance certifications.** SOC 2 etc. are post-team-product, not v1/v2.
- **Built-in cost tracking for LLM API spend.** Out of scope; consume webhook events into your own dashboard.
- **Real-time collaborative editing inside the workspace.** Sessions are shared-tmux, not shared-Cursor. Different problem.
