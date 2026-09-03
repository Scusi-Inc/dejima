# Dejima Roadmap

**Last updated:** 2026-06-28

This is the living roadmap for Dejima. Items are grouped by phase and sized roughly. Status legend: `[x]` = built, `[~]` = in progress, `[ ]` = pending.

**Phases ≠ versions.** The `v1` / `v1.x` / `v2` headings are *planning buckets*, not release numbers. Released builds follow semver: the **first public release is `v0.1.0`**, and we stay in **0.x** — where the CLI/API may still break and `api_version` may bump — until we deliberately commit to API stability at **`v1.0.0`** ("safe to build on"). `api_version` (an integer client/daemon contract) is tracked separately from the semver tag.

---

## 🔖 Versioning / release map

Semver, staying in **0.x** through alpha → beta (API may break; `api_version`
tracks the client/daemon contract separately). **Minor = a feature wave; patch =
fixes.** `1.0.0` is reserved for **API stability ("safe to build on") — the stable
launch line, not a marketing bump.** Retroactive + forward map (semver layered over
the "planning bucket" headings below):

- **0.1** — core runtime: islands, multi-attach sessions, lifecycle, TUI/CLI.
- **0.2** — brokering: Port (intake/export/Ledger), Home Islands, capability broker.
- **0.3** — multi-agent + remote dev: agents-per-island, SSH-façade + VS Code, GitHub
  identities, provider keys, resource controls, self-update.
- **0.4** — governance wave (shipped, v0.4.x): audit log + viewer, team rung
  (tokens/roles/activity feed), audited MCP brokering, Python/TS SDKs + OpenAPI.
- **0.5** — "up in minutes" (shipped, **v0.5.0**): `onboard --provision-host` wizard,
  adaptive first-run + connection-failure prompts, mac-mini runbook; SDK publish
  wiring (PyPI/npm); Keychain secrets + idle auto-hibernate.
- **0.6** — collaboration + completeness (**in progress**): inter-island exchange
  (Lane 5 — mailbox, brokered info link, deny-all action-delegation gate,
  wake-on-message; **code-complete on master, awaiting Minion live-verify**), Port
  read-normalization (shipped), macOS Shortcuts capability adapter (shipped),
  first-class framework adapters (Letta/Hermes/Goose, #21). ← here
- **0.7–0.8** — hardening: trust-on-first-use, per-island egress allow-list,
  observability rollups, webhook-security hardening, watchdog polish.
- **0.9** — public **beta**: feature-complete; web/PWA reference client; optional
  microVM backend; backup/restore.
- **1.0.0** — API frozen, "safe to build on"; SDKs go semver-stable.

Current: **`v0.5.0`**. The **0.6 / inter-island wave is code-complete on master**
(mailbox → info link → action gate → wake-on-message); **`v0.6.0` is gated on your
Minion live-verify** — see **Operator action items** below. Each minor tag is cut only
after the **Release testing & verification** checklist passes on a live host (Minion).

---

## 📋 Operator action items (you)

The things only you can do (on Minion / as owner), in priority order:

> **The 1.0 tag will refuse to build until `fit.txt` is re-verified.**
> `scripts/fit-freshness-check.py` runs in `release.yml` before anything is
> built. Under 1.0 it allows ten releases of drift; at 1.0+ it demands the
> `last revised … against vX.Y.Z` line match the tag exactly.
>
> This is deliberate and it is not a formality. `fit.txt` is public, is linked
> from `llms.txt`, and is what a model reads when someone asks whether Dejima
> suits them. In one week it was wrong in both directions — it claimed the
> Windows path worked after that path regressed, and it nearly shipped "has
> never worked end to end" the day after it started working. Both lines were
> true when written. That is the failure mode: a verdict rots while the file
> goes on looking maintained, and no other check in the pipeline reads prose.
>
> So at launch, re-walk what it claims rather than re-reading it. Confirm the
> unproven list is still the real unproven list, then restamp the line.


1. **Inter-island live-verify (catch-up — it's already shipping).** Follow
   [`operator-tests/inter-island-wave.md`](operator-tests/inter-island-wave.md): deny-all →
   grant → cross-island message → action approve/deny → fail-closed → **wake-on-message (does
   Claude Code actually wake from the tmux nudge / from hibernation?)**. NB: the inter-island
   flow has shipped *unverified on a live host* since 0.5.1, so this is risk-reduction on live
   code, **not a release gate**. Optional after: cut **`0.6.0`** as a cosmetic semver marker
   (binary is identical to 0.5.3).
2. **First Phase-B live test run.** `workflow_dispatch` the **nightly** workflow on the
   `macos-mini` runner with defaults → expect Tier-2 + Tier-3-safe green, Tier-4 green-or-skip.
   Add input `run_system_tests=true` for the service-install/onboard checks; `run_reboot_test=true`
   (recovery access only) for reboot survival. See [`lanes/lane-6-phase-b.md`](lanes/lane-6-phase-b.md).
3. ~~**Runner boot-persistence**~~ — **MOOT 2026-08-16: the `dejimaqa` runner was TORN DOWN**
   for crashing the operator's real `dejimad`. Nothing runs on it, so items 2 and 4 below no
   longer have a host either. Rebuilding is blocked on a diagnosis, not on setup: a
   co-residency guard already landed (`443324e`) and the crash happened anyway, so the guard
   is insufficient or the mechanism is different. Prefer a disposable macOS VM or a spare Mac
   over a second account on the live Minion — the "own daemon, own Docker, never touches
   `aoos`" isolation claim in [`testing/dejimaqa-runner-setup.md`](testing/dejimaqa-runner-setup.md)
   is exactly what failed. Until then **every Tier-3/Tier-4 and clean-Mac row is manual**, and
   fresh-Mac install — known to be failing in the field — has no automation behind it at all.
4. **Clear the standing Minion backlog** (onboarding wizard, terminal auto-reconnect, Keychain
   secrets, idle auto-hibernate, viewer-token scope) — see the Release-testing checklist below +
   the Operator verification queue. **Phase-B automates most of these once it's been run.**
5. ✅ **SDK publish — DONE (2026-06-29).** v0.7.0 + v0.7.1 published to **npm** + **PyPI** +
   the Homebrew tap; the `dejima` name is claimed, `PYPI_API_TOKEN`/`NPM_TOKEN` are live, and
   the tag→publish pipeline ran green.
6. **Housekeeping:** prune stray `.agents/*` / `.claude/worktrees/*` worktrees (see Housekeeping
   below) — confirm each is finished before removing (some may be active agents).
7. **Stand up the competitive drift-checker watchtower** (when you want it live — it's
   built, but agents can't self-spawn). A watchtower is a **Home Island** (headless), so create
   it with `dejima home create --name watchtower --repo <drift-checker config repo> --agent
   headless --cmd "<self-rearming drift loop>"` (NOT `--island` / `--agent claude-code` — home
   islands are headless). **Wrinkle:** the drift-checker is authored as a Claude Code *SKILL*, so
   it must be driven by that headless `--cmd` loop (or an openclaw brain), not run as the SKILL
   as-is. Then grant a per-island GitHub identity (site repo only), an LLM provider key, and an
   egress allow-list (the cited competitor domains + github.com). It then self-schedules a monthly
   drift-check that opens **draft** PRs for a3 to verify. Tooling: `tools/drift-checker/`;
   design: [`drift-checker-design.md`](drift-checker-design.md). Dry run already validated it
   (caught a real E2B drift + a Rivet rename, both since fixed).
8. **Full clean-Mac gate (brew + npm channels) — later, and ⚠️ ONLY on a throwaway box.**
   The **curl** channel is already verified GREEN (21/21 — the v0.7.0 install verdict). To add
   brew/npm coverage, run `scripts/clean-mac/proof-loop.sh` on a **disposable macOS VM or spare
   machine that has NO Dejima daemon installed and is NOT a production host.**
   **🚫 NEVER run it on Minion (or any host running a live operator daemon).** The gate's
   teardown does `dejima uninstall --purge-all` and binds the operator daemon ports
   (`:7273`/`:7274`); run co-resident with a live daemon it will take that daemon **offline**
   (this happened on 2026-06-29 — recovered, no data lost). The **co-residency guard now ships**
   (`refuse_if_live_daemon`, PR #238): the gate hard-refuses if it detects a loaded
   `dev.dejima.dejimad` system LaunchDaemon, a process bound to `:7273`/`:7274`, or a `dejimad`
   owned by another user — so an accidental run on Minion now aborts instead of taking the daemon
   down. Further hardening still open: per-run port/`DEJIMA_HOME` isolation + teardown-on-failure.
   The operating rule is unchanged regardless: **run only on a throwaway box** (the guard is a
   backstop, not a licence to run it on a live host).

*(⚠️ SUPERSEDED 2026-08-16 — the `dejimaqa` host described here was torn down; see item 3.
Kept for the record of what was built, not as current state.)*

*(✅ Done this session — the test-harness operator setup: a dedicated macOS `dejimaqa` user,
a caged self-hosted runner, its own colima Docker, the bot GitHub account +
`TEST_GH_TOKEN`/`TEST_GH_OWNER` + `TEST_AGENT_KEY`. Lane 6 Phase A + B are authored + merged;
only the live dispatch run (#2) remains to exercise them.)*

---

## ✅ Release testing & verification (run before each tag)

Much of Dejima is built in Docker-less build islands, so green CI is necessary but
**not sufficient** — each release is verified live on a host (Minion) before the tag
is cut. (The "Operator verification queue" further below is the *current* batch of
built-but-not-yet-live-verified items; this is the standing checklist.)

**Automated — CI, every PR (must be green to merge):**
- `go build` · `go vet` · `golangci-lint run ./...` · `go test ./...`
- SDK: Python pytest + TS tests; OpenAPI redocly lint; **route-parity** (`server.go` ↔ `openapi.yaml`).

**Live-Docker — on Minion, every release:**
- `scripts/integration.sh` full (Port + multi-agent + MCP sections) — all green.
- Island lifecycle: `init` / `clone` / `exec` / `hibernate` / `wake` / `upgrade` / `purge` + the unpushed-work guard.
- Port: intake / export / traversal-refusal / Ledger verify. MCP: grant → call → `mcp.*` ledgered → revoke.
- Audit: record → `--verify` → `--export csv`; activity feed. Team: `viewer`/scoped token → read OK, `purge` denied.

**macOS-host-specific — on Minion, every release:**
- `service install --system --audit` brings the daemon up audited; reboot leaves it reachable with no login.
- `onboard --provision-host`: sleep disabled, audit on, fresh-host path clean.
- SSH-façade + VS Code Remote-SSH land in `/workspace`; in-island token autonomy (#8).
- **Terminal auto-reconnect:** drop the link (restart daemon / sleep-wake) mid-session → terminal reattaches, doesn't close; Ctrl-b d still exits cleanly.
- Keychain secret storage (no plaintext in config); opt-in idle auto-hibernate fires + wakes.

**Cross-device — Minion ↔ a client (e.g. GIZMO), each release:**
- `connect` / attach / resize / multi-attach; `dejima update` (client + daemon); Windows client.

**Gate:** cut the version tag only after the live checklist passes on Minion.

**Two companion docs:** the **exhaustive automated target** is
[`testing/test-coverage-matrix.md`](testing/test-coverage-matrix.md) (~150 items; Lane 6
drives each row's `Now` → `A`), and the **curated human pass** you run per release is
[`operator-tests/release-acceptance.md`](operator-tests/release-acceptance.md). As the
matrix automates, the human pass shrinks toward just the go/no-go + real-world/UX eyeball.

---

## 🎯 Committed build queue — post-0.1.0 (in order)

The committed forward plan, distilled from the 2026 competitive review (see
`strategy/competitive-gap-assessment.md`). **We're building all of these**; the
numbering is priority order. Detail lives in the phase buckets below; the items'
home is here.

1. **Audit log + read/export + viewer** — the governance moat; lives in Dejima (a tamper-evident record needs engine-level placement). Detail under v1.x. (week+) — **[~] core landed** on `feat/lane1-audit`: opt-in operational log (`api.request` + lifecycle) on the existing hash-chained ledger, optional HMAC keying, a read/filter/export API + `dejima audit` (filters, `--export jsonl|csv`), and a TUI audit pane (`A`). See [`audit.md`](audit.md). Remaining: identity enrichment once Lane 2's who/role lands; live-verify on Minion.
2. **Team rung** — token auth + 3 built-in roles + per-island scope + an activity feed (who, and which agent, did what). The solo→team conversion bridge. (~3 weeks total)
3. **Audited MCP brokering** — deny-by-default grants of MCP servers into an island, every call ledgered. Table stakes (MCP is the default agent tool layer) *and* a differentiator (nobody audits it). (weeks)
4. **Language SDKs (Python + TS) + OpenAPI spec** — `pip install dejima-sdk` / npm. Thin clients over the existing API; generate the request/response client from an OpenAPI spec (API changes = a regen, not hand-edits), hand-write only the PTY-stream ergonomics. Ship now with a "0.x — may change" note; drops example snippets into the API docs for free. (week+ each)

5. **Per-island secrets manager** — managed storage for the access tokens agents' tools need (EAS, npm, API keys), so they stop living in repos, shell profiles, and chat messages. Per-island scope, values never leave the daemon, injected via a parsed (never sourced) read-only mount that rotates live, a deny-list covering loader/interpreter/git execution vectors **and dejima's own `HTTPS_PROXY`** (a secret by that name would silently switch off egress containment), values never displayed after entry, and log masking. Explicitly does NOT hide values from agents in the island — same property as Vault/Doppler/`gh` — and the copy says so. Full design: [`secrets-manager-spec.md`](secrets-manager-spec.md). (days)

Correctly deferred (NOT in this queue): microVM, multi-tenant SaaS, cross-host orchestration, in-Dejima agent orchestration.

### 🛤️ Parallel lanes — up to 4 agents without collisions

The queue splits into up to **four non-overlapping purviews** so several agents
can run at once (each in its own island/worktree — dogfood the product). Split is
by subsystem; shared seams are small and **append-only**. Sub-agents within a lane
are fine.

**Lane 1 — Audit core.** Operational audit log + read/export API + `dejima audit`
+ viewer. Owns `internal/ledger`, the audit endpoints/CLI/TUI, the request-logging
middleware. **Lands first:** the ledger append-interface + read API (gates the
activity feed).

**Lane 2 — Team auth & roles.** Token issuance + 3 roles + per-island scope. Owns
`internal/api/tokenauth.go`, role/scope fields in `internal/project`, `dejima token`.
Puts identity (who/role) on the request context — which Lane 1's audit log consumes.

**Lane 3 — MCP brokering.** `internal/mcpbroker` (modeled on Port/capability),
`dejima mcp`, MCP-grant fields in `internal/project`; writes `mcp.*` to the
**already-shipped** ledger, so it's independent of Lane 1.

**Lane 4 — SDK & clients.** `openapi.yaml`, `sdk/python`, `sdk/ts`, `api.html`.
No daemon code (read-only against the API) — the most isolated lane; safe to run
flat-out in parallel.

**Gating / coordination:**
- *Gate 1 — activity feed:* it's team-facing and needs **both** Lane 1's audit log
  and Lane 2's roles → it's the last item, gated on both. Lane 2's identity-on-request
  should land before Lane 1 enriches audit records with who/role.
- *Gate 2 — shared seams (keep append-only):* register each lane's routes via its own
  `RegisterX(mux)` so `server.go` changes are one line per lane; add config fields in
  **separate files**, since `project.Project` is touched by Lanes 2 & 3; each lane adds
  its own `cmd/dejima` subcommands.
- **Lanes 3 & 4 have no dependency on 1/2 — start immediately.**

Suggested 4-agent start: 1 = Audit, 2 = Team auth, 3 = MCP, 4 = SDK. Dropping to 2:
fold Lane 2 into Lane 1 (shared auth/audit surface), keep MCP + SDK separate. If a
shared file conflicts, commit only your own hunks and rebase.

### 🔀 Inter-agent + inter-island exchange — design now (a conscious thesis revisit)

Requested 2026-06-19: let agents / islands exchange info. **Today:** agents in the
*same* island already collaborate (shared `/workspace` + home; per-agent worktrees),
but there's no messaging primitive, and **cross-island is deliberately blind** (the
containment invariant). Putting this on the roadmap is a conscious revisit of
`positioning.md` (which lists inter-island comms as out of scope) — it needs a design
pass before it becomes a build lane.

The on-thesis way (don't punch a hole) — do it the **Port way:**
- **deny-all by default**; the operator grants a *specific* A→B channel,
- **scoped** (named topics / typed payloads, not ambient visibility),
- **brokered + ledgered** — every cross-island message logged, which makes it a
  governance feature, not just a risk.

Intra-island agent messaging (a lightweight mailbox/blackboard) is lower-risk and
could land first. **To decide before building:** how far we relax the "no
cross-island channel" stance, and whether it stays brokered-only (recommended:
yes — containment stays the default, exchange is an explicit logged grant).
Becomes **Lane 5** once the design + the `positioning.md` update are settled.

---

## 🔭 Later / exploratory (NOT launch-blocking)

Roadmapped but deliberately *not* gating the launch or beta — post-core tracks.

- **A from-nothing Windows acceptance script** *(pre-1.0, not gating)* — walks
  the whole path unattended: `wsl --unregister`, install the client,
  `dejima wsl setup`, `wsl --terminate` and back, `dejima init` producing a
  RUNNING ISLAND that clones, then a Windows reboot. Capturing on every failure:
  `dejima logs`, `systemctl status dejimad`, `journalctl -u dejimad`, the daemon
  log tail.

  **Deliberately second to setup-proving-its-own-durability**, which d3 owns.
  That check runs on EVERY machine on EVERY install and reports to the person
  who can act on it — a user's first install *is* the from-nothing walk. This
  script needs a real Windows box, is destructive by design (unregistering the
  distro is the point — re-running setup on a healthy distro passes today, on a
  machine that was broken for hours), and therefore runs occasionally, on one
  box, when the operator chooses.

  **It would also have found nothing during the 2026-09 Windows work.** Fifteen
  defects, and the ones that mattered were in the daemon, the client and the
  image rather than in the WSL script — see `docs/wsl-windows-postmortem.md`.
  Its value is as a REGRESSION guard on a path that now works and that we will
  keep touching, not as a discovery tool.

  What it covers that setup cannot: a Windows reboot, and starting from an
  unregistered distro. Those are the least likely regressions, which is why this
  is roadmapped rather than queued.

- **A situational island primer** *(the static half is fixed and guarded; see
  `image/island-primer.md`)* — agents get told what is possible IN GENERAL, and
  cannot tell "not granted yet" from "does not work". The static primer said an
  island token "CANNOT reach other islands" while `tokenauth.go` grants
  `/link/send`, so every agent in every island believed cross-island messaging
  was impossible **by design** — and never saw the refusal that names
  `dejima link grant`, because nobody got that far. A second instance: four Port
  routes are granted and Port appeared only in a "don't reach host files"
  paragraph. Both corrected, and a test now fails if a granted capability goes
  unnamed. **What remains is that the primer is static.** The daemon knows this
  island's actual link grants, Port scopes, secret names and co-residents at
  launch; rendering them below the primer turns "I believe this is impossible"
  into "this isn't granted yet, and here is the ask" — the same
  absence-rendered-as-absence distinction the grants pane needed. Mechanism
  decided: the primer, because it is the only agent-agnostic surface (a Claude
  Code skill would leave codex/openclaw/letta/goose uncovered). MCP is better for
  discoverability and duplicates the CLI, so it is an addition to revisit later,
  not a replacement.

- **Adopting agents Dejima didn't launch** *(design written; **DISCOVERY
  DEFERRED TO POST-1.0**, 2026-09-01 — see
  [`agent-adoption.md`](agent-adoption.md))* — changes the pitch from
  "run your agents in containers" (a migration, which asks for a decision before
  giving anything back) to "whatever you're already running, Dejima can see it,
  ledger it, and gradually pull it into containment" (an adoption ramp). Someone
  with six agents loose in terminals gets value on day one without moving
  anything. Matters here specifically because install is our worst friction.
  **The risk is the defect this codebase demonstrably produces**: containment
  becomes a spectrum while every claim we make about it is binary — the same
  shape as the grants pane, `secret rm`, and "every agent is walled off from the
  other agents" sitting live on six pages including the homepage. So the
  structural distinction is built FIRST: containment level as a non-optional
  data field (never a property of which source answered), a separate section
  rather than a badge, a grants view that says "observed, not gated" instead of
  four empty arrays that read as sealed, and ledger entries marked
  **self-reported** — an adopted agent's ledger is its own account of itself and
  omission is trivial. Phase 2 (graduation into an island) is specified but not
  started; it depends on a preflight diff of what the agent will lose and on the
  dirty-worktree guard, since a graduation that clones fresh destroys uncommitted
  work exactly the way `agent rm` did.

  **DEFERRED POST-1.0 (operator, 2026-09-01): DISCOVERY.** The piece that finds
  agents Dejima did not launch — transcript directories and running processes,
  explicit and operator-initiated. Everything else in Phase 1 is either shipped
  or in flight; discovery is what would make it *do* something, and it is the
  part that should wait.

  Why it waits, and the reasoning matters more than the decision because it will
  be re-argued:

  - **Nobody who uses Dejima today has agents to discover.** Its value is for
    someone already running Claude Code loose who installs Dejima and wants their
    existing work reflected rather than starting over. That is an ACQUISITION
    argument, and acquisition is not where 1.0 is.
  - **It is the only feature that needs host access with no grant.** Every other
    host-file path goes through Port — scoped, brokered, ledgered. A discovery
    scan is a filesystem walk outside that machinery, in a product whose claim is
    that host access is audited. Defensible (read-only, operator-initiated) and
    still the first exception, which is expensive to explain.
  - **It is permanent maintenance on other vendors' internals.** Transcript
    layouts and process signatures belong to Anthropic, OpenAI and Block, change
    without notice, and differ per framework.

  The spec argues Phase 1 is worth shipping alone because *observing is easy and
  gating is hard*. That is true about COST and does not establish VALUE, which is
  where it fails today.

  **What is already banked, and is not wasted** — this is what makes deferring
  cheap rather than a write-off. All of it is the expensive-to-retrofit half:

  - the containment level as a non-optional field whose zero value cannot
    reassure (retrofitting a level onto a shipped model, after surfaces read it,
    is precisely how the bugs we spent two weeks removing were made);
  - the naming, which caught two collisions before they shipped — `observed` vs
    the existing `adopt` verb, and `witnessed` vs `observed` a second time in the
    ledger, reached independently three days apart by the identical route;
  - ledger provenance with `omitempty`, which found a real bug: a field
    serialising on every row rewrites the hash of every historical row, so
    `dejima audit --verify` fails on a ledger nobody touched — a failure that
    reads as TAMPERING.

  **STATUS OF WHAT DID SHIP: DESIGNED AND UNPROVEN, NOT DONE.** d3's flag, and it
  belongs here because "shipped" will otherwise be read as "working". The
  containment field, the `GET /v1/agents/observed` collection and the provenance
  levels are all landed, all correct as far as unit tests and mutation checks can
  establish, and ALL UNEXERCISED BY REAL DATA — no observed agent has ever
  existed. That is not a criticism of the deferral; the model landing first is
  exactly what the spec asked for. It means the first real observed agent is the
  thing that tests this half, and until then it should be described as designed.

  A consequence for the surfaces: `registered:false` distinguishes "none are
  registered" from "Dejima has no way to learn about one", and with discovery
  deferred indefinitely the second is the permanent state. A section rendering
  "no observed agents" would be claiming Dejima looked. It must either not render
  while `registered` is false, or say that Dejima cannot yet see agents outside
  islands.

  **The condition for picking it up again is not a date.** Discovery is the
  prerequisite for Phase 2 (graduation), and graduation is the half with an
  obvious story: *you are already running agents unprotected, here is the
  one-click way to fix that*. If graduation becomes the bet, build discovery
  first and this deferral ends. If it stays a maybe, discovery is scaffolding for
  a building nobody has decided to put up.

- **Dejima running locally on Windows** *(research done, see
  [`windows-native-daemon.md`](windows-native-daemon.md))* — today `local` in the
  connection switcher dead-ends on Windows: `dejimad` ships for darwin/linux only
  (`Makefile`), so a Windows user can only be a client of someone else's daemon.
  There is a real audience for isolated islands on the Windows box itself.
  Research finding: the daemon is far closer than "Unix-only" implies — it
  already **cross-compiles and `go vet`s clean for windows/amd64** — and there is
  exactly ONE functional blocker, `startPTY` in `internal/bridge/session.go`,
  because `creack/pty`'s Windows build is a stub returning `ErrUnsupported`. Two
  callers, one of them the optional `--host-terminals` feature. Three paths:
  (A) a ConPTY `startPTY` on Windows — smallest, but puts ConPTY on both ends,
  which is the class of bug behind the left-column smearing; (B) drop the host
  PTY entirely and use the Docker Engine API's exec+attach hijacked stream — a
  real `internal/bridge` refactor, but removes creack/pty from the session path
  on *every* OS and is the only option that leaves Unix better than it found it;
  (C) WSL2, already built on `agent/d4` (`35f929c`). Sequencing note: Docker
  Desktop runs Linux containers in a VM regardless, so a native daemon is a
  packaging/UX win, **not** an isolation win — C already delivers the capability,
  so ship it, learn from real Windows users, then decide whether B earns the
  refactor. Carries an unresolved design question either way: the local socket's
  "filesystem-trusted, acts as OWNER" model (`clientForHost("")` ignores
  `DEJIMA_TOKEN`) has no Windows equivalent and must be settled before a Windows
  daemon is safe to expose. Motivated 2026-08-12. Owner: daemon. (weeks)

- **Voice dictation — rebuild (currently DISABLED)** *(engine built, surface stashed)* —
  the local-transcription engine (`internal/voicein`: mic capture → whisper.cpp →
  transcript inject) is built and works **on macOS/Linux**, and the CLI
  (`dejima voice`, `voice install`, `voice status`, `voice device`) is intact but
  `Hidden`, off the help/settings surface. Disabled 2026-07-23 because the flow
  is half-wired for the primary user (Windows), where it misleads more than it
  helps. Re-enabling means, IN THIS ORDER (verifiable-first, since the Windows
  paths can't be tested from a macOS dev box):

  1. **In-session voice chord** — a client-side key intercept in `dejima connect`,
     exactly like the Ctrl-V image-paste chord: press a chord (default TBD,
     configurable via `DEJIMA_VOICE_KEY`, disable-able) → record on the client
     mic → whisper transcribes locally → inject the transcript into the agent's
     prompt as text. Press-to-start / press-to-stop (a pty has no reliable
     key-release). Build + prove on macOS/Linux FIRST. **Recording indicator:**
     can't draw over an agent's alt-screen (same reason `sessionNotice` is
     suppressed) — use the terminal TITLE bar via OSC ("🎙 recording…" →
     "transcribing…" → clear), which survives alt-screen. **Chord collision:**
     every Ctrl-key risks shadowing an agent binding; pick a safe default, make
     it configurable and disable-able.
  2. **Windows voice install automation** — the actual blocker for the Windows
     user. Today `voice install` on Windows only PRINTS manual steps. Automate:
     `winget install Gyan.FFmpeg` + download a whisper.cpp Windows release
     (same machinery as the model download). Known hazards, all real: whisper.cpp
     has **no stable winget package** — versioned release zips with inconsistent
     asset names, CPU-variant selection (wrong AVX build CRASHES), possibly
     missing DLLs, and SmartScreen/AV quarantine; **PATH doesn't refresh** in the
     running process after winget (the gh-in-tmux trap again — installed tools
     stay invisible until a shell restart, so say so loudly); winget may need
     **UAC**. Pair with the `dejima voice device` picker (dshow needs a NAMED mic
     — headset vs webcam array — no default).

  Meta-risk to plan around: the entire Windows path is UNTESTABLE from the dev
  box, and voice already shipped broken on Windows once for exactly this reason.
  Expect several test round-trips with the operator; build the verifiable core
  (#1 on macOS) solid before touching the blind part (#2).

- **Brokered secret access — per-use logging + approval gating** *(post secrets-manager v1)* —
  these are ONE feature, not two. With environment injection there is no read event to observe:
  a tool reads `EXPO_TOKEN` from its own process memory and nothing crosses the daemon, so
  "agent X used this secret at 15:42" is unobtainable by construction. Both per-use audit and
  per-use approval become possible only if the agent must *ask* (`dejima secret get NAME` over
  the island token API), which also unlocks rate limiting and reuses the pending-actions
  machinery in [`action-gate-spec.md`](action-gate-spec.md). The `require_approval` field is
  stored from secrets-manager v1 so this lands without a migration. Cost: tools don't fetch
  natively, so the agent wires it by hand and the value re-enters that shell's environment —
  brokering removes *ambient* exposure and buys the audit trail; it does not make a value
  unreadable. See [`secrets-manager-spec.md`](secrets-manager-spec.md) § Deferred.

- **Native multi-agent tiled/split live view in the TUI** *(post-v1, consider)* — today,
  seeing several agents' *live* terminals at once needs either a terminal with a "new-window
  backend" (iTerm2 / Windows Terminal, which `openAgents` drives) or manually splitting your
  own tmux and running `dejima connect <island> --agent <id>` in each pane. A raw `Ctrl-b`
  split just opens a plain shell — tmux has no notion of Dejima agents. For a "run a fleet"
  product the multi-agent view should be one keystroke: select N agents in the TUI → tiled /
  split live panes in a single window, no manual connect-per-pane, no dependence on the
  terminal's new-window support. Pairs with the QoL-positioning push (if we put "run many
  agents" on the homepage, seeing the fleet at once shouldn't be manual). Options to weigh:
  an embedded multiplexer in the TUI vs. driving tmux splits for the user vs. a richer
  new-window fan-out. Motivated 2026-07-03. Owner: TUI. (days)
- **colima memory sizing in onboarding** — `dejima onboard --provision-host` installs
  Homebrew/Docker/colima but doesn't *size* the VM, so a fresh install gets colima's
  default: island-heavy hosts OOM (see [OOM incident #23]), big hosts under-use their RAM.
  Add a step that detects host RAM (`sysctl hw.memsize`) and sets `colima start --memory <N>`
  with a sane default (≈half RAM, leaving macOS headroom), promptable/overridable; surface
  the current size in `dejima doctor` and offer a resize path so it's adjustable later
  (a resize needs a colima stop/start → bounces islands, so warn). Extra credit: **multi-VM
  awareness** — a *second* per-user colima on the same host (e.g. a teammate's per-account
  fleet) must split RAM, not each grab half; the common single-VM case is the immediate win.
  Motivated 2026-07-02 standing up a teammate's per-account fleet on a shared 24GB mini
  (had to hand-run `colima start --memory 8` and manually keep the primary VM smaller to
  leave room). Small, self-contained backend/CLI task. (hours)
- **Ambient / monitoring agents** — scheduled, long-running monitor/assistant agents (repo
  watch, email/feedback triage, competition + news/industry digests), run under the owner's
  real identity (not the `dejimaqa` test account), with **brokered + audited** access to
  real accounts (Gmail/web/repo via MCP/Port). Right agent per job (Letta for memory-heavy
  monitors, Hermes/Gmail-MCP for email, OpenClaw Home Island as coordinator — not Claude
  Code, which is for coding). The enabling new primitive is a **scheduler** (cron-wake
  islands, the time-driven twin of wake-on-message); actions route through Lane 5's
  action-delegation gate. Design + phasing: [`ambient-agents-design.md`](ambient-agents-design.md).
  **First built instance:** the competitive **drift-checker watchtower** (`tools/drift-checker/`),
  a self-scheduling Claude Code agent that re-verifies the comparison pages' cited facts and
  opens **draft** PRs on drift (design: [`drift-checker-design.md`](drift-checker-design.md)).
  It validates the pattern today by self-re-arming a short harness wakeup (the harness cron
  expires ~7d and `ScheduleWakeup` is clamped ≤1h, so it re-arms each cycle). The durable
  **scheduler primitive** it wants — daemon-level `dejima wake --at/--every`, symmetric to
  idle-hibernate and covering headless agents too — is spec'd in
  [`scheduled-wake-spec.md`](scheduled-wake-spec.md) (filed with backend); once it lands the
  watchtower hibernates between runs instead of staying resident.
- **Randomized soak / combination "backbone"** — the stress layer above the deterministic
  full-feature suite: run *valid* lifecycle/agent/Port/link op-sequences in random orders +
  combinations, repeatedly, with invariant checks after each (no orphan containers/worktrees,
  daemon healthy, ledger verifies, no zombies). Catches state bugs the happy-path suite
  misses. Build after the deterministic suite ([`testing/full-suite-design.md`](testing/full-suite-design.md)).
- **Island PID-1 unification — retire "primary" entirely** *(target pre-1.0)* — the TUI/CLI
  no longer has a privileged "primary" agent (Enter on an island opens a contained shell;
  agents are explicit), but the container entrypoint still is: a *headless-first* island runs
  its first agent as PID 1, so that one agent can't be freely removed and the island can't be
  agent-less. Fix: always use the keepalive (`tail -f /dev/null`) entrypoint and launch every
  agent — interactive *and* headless — through the supervised `docker exec` path that already
  exists for non-primary agents, so no agent is ever special. Feasible (it deletes a special
  case rather than adding capability) but touches the island lifecycle (provision/wake/
  entrypoint/logs), so it wants live verification. Migration is lazy-on-recreate (interactive
  islands convert for free; headless-first need a recreate to avoid double-launch). Full
  design + migration plan: [`island-pid1-unification.md`](island-pid1-unification.md).

  **Status 2026-08-13: code is written and parked, waiting on a host window, not on
  a decision.** PR #143 implements it (5 files, +106/−108) and is green on
  build/test/vet/lint/openapi-parity — but it is DRAFT/do-not-merge because CI only
  compiles Go and never spins a container, so every lifecycle claim in it is reasoned
  rather than observed. Six acceptance steps are listed on the PR and none are done;
  they need real island create/destroy on a real host and cannot be run from inside
  an island. Three things to settle when it's picked up, in this order:
  1. **Rebase and reconcile with a0bd706.** Both edit the same regions of
     `internal/api/server.go`. The PR deletes `DEJIMA_LAUNCH`, which is exactly what
     a0bd706's resume threads through — so `createContainerForProject`'s
     `LaunchFor(resume)` becomes dead code and `reconcileAgents(…, resume)` becomes
     the whole mechanism. That is a better end state, not a conflict to paper over.
  2. **Repoint `dejima logs <island>`** (no agent id) — the PR flags, and does not
     fix, that a headless-first island's output moves to `headlessLogPath(id)`. It is
     a knowing regression until done.
  3. **Rebuild the image + fleet upgrade**, since `image/start.sh` changes.

  Doing this also closes **#333** (wake can't resume an agent's conversation) for free
  and in the right way: #333 is blocked because `DEJIMA_LAUNCH` is frozen into
  container env at creation and wake reuses the container, and Path B removes that env
  entirely — resume becomes a launch-time parameter, so the problem stops existing
  rather than being worked around. Note the priority shifted on 2026-08-12: a0bd706
  made `dejima upgrade` relaunch *and* resume every agent, so the only remaining gap
  is wake, which fires far less often than upgrade. This went from "fixes what's
  biting us" to "deletes a special case and closes a corner" — still worth doing,
  no longer urgent.
- **Multi-host / distributed (discuss later)** — running agents across *multiple* hosts
  (several Mac minis / servers / cloud). Today Dejima manages one host, so a fleet-of-hosts
  is a wrapper-app concern. Open question worth a deliberate decision: should the substrate
  natively **federate hosts** (a "fleet of hosts" alongside the "fleet of agents on one
  host"), or stay single-host and leave cross-host orchestration to a control plane above?
  Relates to inter-island exchange (cross-host would extend it). Park for a design talk.
- **Resume the agent session on wake (don't cold-start)** — today hibernate stops the
  container, killing the tmux server + agent process; only the workspace volume persists, so
  wake recreates the container and `start.sh` launches a *fresh* agent — work resumes but the
  agent's conversation/context is lost. Fix = a **per-adapter resume-on-wake seam**: on wake,
  restart terminal agents with their resume flag (Claude Code `--continue`/`--resume`, Codex
  equivalent) so the agent reattaches its prior context (a new tmux is fine). Pragmatic over
  `docker pause` (doesn't free RAM) / CRIU (fragile). Matters for interactive/terminal agents;
  headless/stateless ones (e.g. the watchtower) cold-start fine. Pairs with the scheduled-wake
  primitive above.
- **Per-user daemons on one host (shared-fate consideration)** — one host runs **one** system
  daemon (`1 dejima = 1 server`), so separate OS accounts on the same machine still *share* it:
  the operator's `update`/`restart`/crash blips every user on that host (felt during the
  2026-06-29 incident). For a team this is fine — teammates **join the fleet** with island-scoped
  tokens. The thesis line is **one operator per host**; for true independence the honest answer is
  a **separate host/VM**, not a second daemon (which collides on `:7273`/`:7274`). Per-user-port
  daemons are buildable but arguably off-thesis (containment favors separate hosts for separate
  operators) — park unless demand is real. Relates to the multi-host question above.

---

## 🌐 Website + docs backlog

The site and README lag the shipped feature set — found 2026-07-22 while adding
the secrets manager to the feature list. Both of these are user-facing, on by
default (egress) or fully built (voice), and appear in **neither** `README.md`
nor `index.html`:

- [x] **README `What you get`** — added the egress gate + voice dictation entries.
- [ ] **`index.html` `#features`** — same two, plus the site has no mention of
      egress control at all, which is one of the stronger containment claims
      (see every destination an island reaches; allow/deny by host, no restart).
- [ ] **Secrets manager** — announce on the site once built. Copy must state
      plainly that it does NOT hide values from agents in the island; the
      feature is repo/chat hygiene, central rotation, and audit, not a boundary.
      Overclaiming here would be worse than shipping nothing, because operators
      would put things in it they shouldn't. See
      [`secrets-manager-spec.md`](secrets-manager-spec.md).
- [ ] **Voice: platform table** — the site should say voice runs on the machine
      with the microphone (not the daemon host), and that macOS/Linux install
      via `dejima voice install` while Windows needs ffmpeg + whisper.cpp by hand.
      This confused an operator driving a Mac mini from a Windows client.
- [ ] **Audit ledger + SSH façade** — also shipped, also absent from the site.
      Worth a sweep of `README.md` § `What you get` against the actual CLI verbs
      rather than fixing these one at a time as they're noticed.

---

## 🧹 Housekeeping (one-off, when next at the host)

A finished lane agent left a worktree squatting the `master` branch ref
(`.agents/d7`), which caused a tag to land on the wrong commit once. Clear it when
no agent is attached:

```bash
git -C <dejima checkout> worktree remove .agents/d7   # frees the 'master' branch ref
git -C <dejima checkout> worktree prune
```

---

## 🧑 Operator verification queue (built, needs a live run)

These shipped to `master` with unit/security review but can't be exercised from the
build island (no live Docker/macOS host here). Run them on Minion and feed findings back.

- [ ] **TUI verify pass v0.6.1 → v0.6.9 (consolidated)** → [`operator-tests/v0.6.9-verify.md`](operator-tests/v0.6.9-verify.md).
  One eyeball pass covering agents-by-name, usage signals + near-cap flags, the
  name-collision notice, wake-on-message, tab titles, visual-identity/keys, and
  the `used`-counter `link ls` question. Ping a2 with results (a2 relays to
  d5/the owner + closes the counter with a1). Supersedes the stale v0.6.1 doc.
- [ ] **Inter-island exchange (Lane 5, Phases 1–3.5) — full live-verify** → gates `v0.6.0`.
  Run [`operator-tests/inter-island-wave.md`](operator-tests/inter-island-wave.md) on Minion:
  deny-all default, grant + cross-island delivery (tagged + ledgered), action-gate approve/deny
  (gated + audited), agent-can't-self-approve, fail-closed, and **wake-on-message** (Claude Code
  wakes from the tmux nudge + from hibernation; a busy agent is not interrupted mid-turn).

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

- [x] **Repo pushed to `github.com/aoos/dejima`** — install URLs resolve; releases through v0.5.0 published.
- [x] **GitHub Pages enabled from `master:/(root)`** — live at `aoos.github.io/dejima/`; custom domain `dejima.tech` set via `CNAME` (DNS wiring is the remaining user step — see [`docs/distribution.md`](distribution.md)).
- [ ] **🧑 USER TASK — Create `aoos/homebrew-dejima` tap repo** (then the release CI auto-bumps `Formula/dejima.rb` on each tag — see [`docs/distribution.md`](distribution.md)). (15 min, free)
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
- [~] **Custom domain — `dejima.tech` registered + `CNAME` committed.** Remaining 🧑 USER TASK: add the GitHub Pages A/AAAA records (and `www` CNAME) at the DNS provider, then set the domain in Settings → Pages. See [`docs/distribution.md`](distribution.md). (~30 min)
- [ ] **Site: "Is Dejima right for you?" copy-paste prompt** — zero-backend widget on `index.html` that copies a crafted prompt (summarizes Dejima, references the site URL + `api.html` API docs, asks the visitor's situation, asks the model whether Dejima fits) to the clipboard for the visitor to paste into their *own* Claude/ChatGPT. No hosted inference, no bundled weights — honors the no-SaaS / no-weights non-goals. (hours)
- [ ] **Site messaging refresh** — landing copy lags shipped work. Surface: SSH-façade → VS Code/Cursor Remote-SSH on-ramp, per-daemon GitHub identities, trustworthy self-update, host terminals, capability brokering, `clone`, panic / unpushed-work guards. Strongest under-told narrative: *"turn a Mac mini into a personal agent server you edit in your real IDE."* Diff the recent commits against the site copy and propose edits before applying. (hours)
- [ ] **Submit to homebrew-core** — eventual `brew install dejima` without the tap prefix. Months of stewardship; defer until v1.x has users. (months)
- [ ] **`dejima update` epic** — one role-aware command that pulls, installs, and restarts (server + client). Consolidates the four former sub-items below.
  - **V1 (dual-mode, local)** — auto-detect: in a git checkout with Go → *source path* (`git pull` + `make install` + restart); else → *release path* (download the `GOOS/GOARCH` asset, checksum-verify against published `SHA256SUMS`, atomic self-replace via go-update/selfupdate — Windows = rename-aside swap since a running `.exe` can't be overwritten). **Role-aware:** client swaps `dejima` only; server swaps `dejima`+`dejimad` then `dejima service restart` (islands survive via `AdoptExisting`; only live attaches blink). **Flags:** `--check` (dry-run/availability), `--yes`, `--channel stable|edge`, `--notify` (fire a webhook before any daemon restart). Reuses release CI + `SHA256SUMS` + `service restart` + events. ~1–1.5d. **Prereq for the *client* half: a release cadence** (even auto-tagged `v0.x` edge builds from CI) — a binary-only client can't build from source.
  - *folds in:* **self-update** (client download+verify+swap → the release path) · **update-available check** (`--check` / daily-opt-in → `update.available` webhook) · **stable vs edge channels** (`--channel`).
  - **Deferred (v2): remote daemon update** — GIZMO → Minion daemon self-update over an *authenticated admin endpoint* (process-restart-behind-launchd + authz; ties to the per-island token work). Notify-then-apply, never silent.
- [x] **Self-update restart no longer yanks attached terminals (gate shipped `45de8ef`).** **Symptom diagnosed 2026-06-19:** "terminal tabs keep closing, infrequent, as if daemons restart but not OOM-killed." Root cause was **not** a crash — it was the self-update itself: each TUI `[U]` (and a new `master` commit during dogfooding) runs `dejima service restart`, which kills the daemon and drops **every** attached terminal fleet-wide (containers/tmux survive — `restarts=0`, `oom_kill=0`, clean `shutdown signal received`, version march in `~/Library/Logs/dejima/dejimad.err.log`, no panic). **Fix:** gate the apply server-side in `handleAdminUpdate` — unless `Force`, defer while any client is attached (`s.attachedSessions()`); response carries `Deferred`+`AttachedClients`; the TUI re-prompts to force or detach-and-retry. Gating in the daemon (sole authoritative session count) covers every caller. Also fixed a latent bug surfaced by the test: `presenceHandle` was a zero-size `struct{}`, so every `&presenceHandle{}` aliased `runtime.zerobase` → distinct attaches collided on one map key, silently capping presence (and `RevokeAll`) at one client/agent. **Open follow-ups (NOT high pri — ship-and-see; revisit if the manual retry annoys in practice):**
  - [ ] **Auto-apply a deferred update when the last terminal detaches** (the better of the two). Hook the existing `last-client.detached` event so a queued update applies itself once idle — removes the manual retry. Musts: re-check the attached count at apply time (the gate already does, so it's free), a visible "update pending — applies when idle" indicator, and a cancel. Caveats: the daemon self-restarts unattended (by design; low blast radius since tmux/containers survive), and the pending intent is in-memory (lost across an unrelated daemon restart → next `[U]` re-arms). *Lean: do this one.*
  - [ ] **Extend the gate to the local `dejima update` CLI** (`cmd/dejima/update.go` → `selfupdate.ApplySource`, which bypasses the daemon). *Lean: skip.* It's an explicit foreground command you run and watch, so deferring fights intent; and gating couples the CLI to a reachable/responsive daemon socket — but a wedged daemon is often *why* you're updating by hand. If ever done: warn + `--force` + degrade-open when the socket's unreachable, not a hard gate. Per the logs the churn was the TUI/admin path, not the CLI.
- [x] **Container watchdog goroutine** — daemon polls `Status()`+`Inspect()` every 30s and emits `container.crashed` on unexpected exits (running→stopped/missing) and on restart-count climbs (flapping under `--restart unless-stopped`, e.g. repeated OOM kills); payload carries status/exit_code/oom_killed/restart_count/reason. Edge-triggered (no re-emit at steady state), primes silently on first scan, and stays quiet while panic mode stopped everything deliberately. (`internal/api/watchdog.go`)
- [x] **`dejima upgrade <name>`** — recreate a container against a fresher island image while preserving volumes (also `--all`, and the `u` key in the TUI). Pairs with **`dejima image build`**: the build context is embedded in `dejimad`, so the image rebuilds on the daemon host with no source checkout; missing images auto-build on first `dejima init`.
- [x] **`dejima panic`** — stop every island immediately; write a `~/.dejima/PANIC` flag preventing auto-restart until removed. **Shipped:** `dejima panic` (engage), `--clear` (remove flag + restart islands meant to be running), `--status`; daemon `GET/POST/DELETE /v1/panic`; `AdoptExisting` refuses to auto-start while the flag is set (survives a daemon restart); state surfaced in `/overview`, `doctor`, and a TUI alarm banner. Emits `daemon.panic-engaged`/`daemon.panic-cleared`.
- [x] **Unpushed-work guard on `purge` + `dejima uninstall`** — **guard:** `purge` / `DELETE /v1/islands` inspect `/workspace` (dirty + ahead-of-upstream) and refuse with 409 unless `--force`, naming the at-risk file/commit counts and branch; a non-running island can't be verified so it also requires `--force`. A blocked TUI purge offers a force-purge confirm. **uninstall:** `dejima uninstall` runs the whole clean-removal sequence — pre-flights the unpushed-work guard across *all* islands (so it never half-uninstalls), then guarded-purges every island → uninstalls the service → removes the dejima/dejimad binaries → deletes `~/.dejima`. Confirms first (unless `--yes`); `--force` bypasses the guard, `--keep-data` preserves `~/.dejima`; degrades gracefully on permission errors (suggests `sudo rm`).
- [x] **`dejima refresh-creds <name>`** — covered by `dejima upgrade <name>`: recreating the container re-assembles all credential mounts (and re-materializes the Claude seed) without touching the workspace. `dejima auth push` handles getting fresh Claude credentials onto the daemon host in the first place.
- [x] **`dejima clone <name> <new-name>` — copy an island (with its credentials)** — **shipped:** `POST /v1/islands/{name}/clone` + `dejima clone` duplicate an island: new config + byte-for-byte copies of its workspace **and** home volumes via `runtime.CopyVolumeData` (throwaway container, `cp -a`, source mounted read-only). Volumes are populated before the container starts so `start.sh` sees `/workspace/.git` and skips re-cloning. Owner/tags/agents carry over; **Title and Port grants are deliberately dropped** (clone shows its own name and starts deny-all — never silently inherits host access). Caveats below stand (host-bound tokens, duplicated `~/.claude` state). Original scope note retained: Because all creds / permissions / tool-auth live in the per-island `/home/dejima` home volume ([`multi-agent-spec.md`](multi-agent-spec.md) §6), cloning carries them along for free — so this is the natural "copy an island with everything" primitive, and it also underpins a *true* island rename (clone to a new name + delete the old; today rename is a cosmetic display **title**, since Name is immutable infra identity). Caveats: device/host-bound tokens may not survive a cross-host clone, and duplicating `~/.claude` duplicates session/runtime state (the §6 shared-home note). **Copying *just* credentials/permissions to another island is deliberately out** — it reintroduces the per-tool-token-path enumeration the whole-home volume was chosen to avoid; if ever needed, extend the `dejima auth push` seeding per-tool instead of doing volume surgery. (1-2 days)
- [x] **Secrets at rest via Keychain / Secret Service** — shipped. New `internal/secrets` stores small secrets in the macOS login Keychain (`security`) or Linux Secret Service (`secret-tool`), with a 0600 `~/.dejima/secrets/keystore.json` fallback. Every op degrades to the file store when the keychain is locked/absent (the headless-boot caveat below), so it never hard-fails. Wired to **webhook HMAC secrets**: they no longer persist in `webhooks.json` (legacy plaintext is migrated into the store on load and scrubbed), and `GET /v1/events/subscriptions` now redacts them. The credential stores (`providercreds`/`githubid`/`porttoken`) deliberately stay file-based for now — they're create/boot-critical, where the file path is the *safer* default; they can adopt `internal/secrets` once the locked-keychain story is proven on a `--system` install.
- [x] **Idle auto-hibernate** — shipped, opt-in. `dejimad --idle-hibernate <dur>` (env `DEJIMAD_IDLE_HIBERNATE`; 0/default = off) runs a daemon-side sweeper (`internal/api/idle.go`) that hibernates a running island after it's been continuously idle — **no attached client and no live agent process** — for the threshold, re-checking under the island lock before stopping. Reuses the presence + agent-liveness signals; never touches an island with a live agent (an idling Home-Island brain is safe). Emits `island.hibernated{reason:"idle"}`.
- [~] **`dejima onboard --provision-host`** — Mac-mini-as-home-server provisioning wizard. Walks through Energy Saver / Sharing / Homebrew / Tailscale / Docker / SSH config / `.zshenv` (auto-doing what it can, instructing for GUI-only steps), then hands off to the existing Dejima onboarding. Closes the "I just unboxed a Mac mini, what now?" gap. **Strategically important: shifts positioning toward "the easy way to turn a Mac mini into a personal AI agent server."** Full plan: [`docs/host-provisioning-plan.md`](host-provisioning-plan.md). **Built** on `feat/onboard-provision-host`: 6-phase detect→act→verify state machine (`cmd/dejima/provision.go`) with resume + `--yes` + `--reset`; never-sleep `pmset`, Homebrew/Tailscale/Docker install, VM right-size (`doctor --fix`), `.zshenv` PATH + Remote Login, then `service install --system --tcp :7273 --token-tcp 127.0.0.1:7274 --audit`. Companion runbook: [`docs/mac-mini-host-setup.md`](mac-mini-host-setup.md). Live-verify on a real Mac pending (Minion). (~1 week)
- [x] **Adaptive first-run prompt** — Detect server vs client context (Docker + dejimad binary present? DEJIMA_HOST set? daemon reachable?) and ask a context-specific y/n/N question instead of the generic "first time?" — shipped: `detectFirstRunContext` branches the no-args prompt into configured (→ straight to TUI), client-unreachable (→ troubleshooter), fresh-macOS-host (→ offer `--provision-host`), or generic. Same marker / never semantics.
- [x] **Connection-failure offer** — When the CLI hits a "daemon unreachable" error for the first time on a host that has `DEJIMA_HOST` set, surface a one-shot *"Want help troubleshooting the connection?"* prompt. Shipped: `maybeOfferConnectionHelp` at the `main()` error choke point + `runConnectionTroubleshooter` (Tailscale / host-TCP checks); one-shot via a `~/.dejima/conn-help-offered` marker.
- [ ] **Webhook security hardening** — the URL itself is a secret today. Improvements: (a) require a strong HMAC secret by default rather than as opt-in, (b) optional bearer-token auth on the receiver, (c) generate a high-entropy ntfy topic suffix automatically when user types a bare topic name, (d) interactive secret prompt during `dejima service install`. Already partially shipped (HMAC + interactive secret prompt); the rest is roadmap. (1-2 days)
- [ ] **Notification routing — per-island subscriptions (tenancy)** — *designed 2026-08-18 (d3 + d1), not built. Build before the content tier below; they are separate bugs.* `events.Subscription` (`internal/events/manager.go:24`) is `{ID, URL, Secret, Events, CreatedAt}` — **no island field**, and `Emit` filters on event *type* only, so **every subscription receives every island's events**. That breaks the work/personal and freelance-client cases: register a webhook for client A and one for client B and both see everything. **Bounded, not a containment break:** `POST /v1/events/subscribe` is `capOwner` and `accessDeny` is the token-auth zero value (`subscri` appears nowhere in `tokenauth.go`), so a contained agent *cannot* create a subscription — this is operator-to-operator between their own webhooks. Design: **exact match**, target stored as an **opaque selector string** from day one so `group:acme` lands later with no migration. **Name-prefix matching is ruled out permanently** — a rename silently re-routes and a new `acme-internal-audit` joins that client's Slack by existing, which makes the convenient action and the tenancy-breaking action the same action; membership must be *declared*, never inferred from a display string. **Migration is the dangerous part:** existing subs must become an *explicit* all-islands selector, surfaced as "this webhook receives EVERY island" — `empty = all` preserves the leak invisibly, `empty = none` silently stops every existing webhook, and a notifier gone quiet is indistinguishable from nothing having happened. An **unrouted-islands surface is part of the feature**, not a follow-up: exact match without it fails closed *silently*. (2-3 days)
- [ ] **Notification content tiers — what crosses, per subscription** — *designed 2026-08-18; depends on the routing item above.* A notification reading "Claude wants to run `rm -rf` in /workspace/acme-client" ships a client name and file paths to a third party, voiding "your code never leaves your box" in the one feature whose job is to be helpful. Tiers: `full` (event + island + agent + question text) · `minimal` (event + island + agent *label* + deep link, **no content**) ← **default** · `opaque` (count only). **`minimal` means DROP `Event.Payload` wholesale and rebuild from the daemon-authored typed fields** (Type/Island/Agent/AgentLabel/Timestamp) — *not* redact it: `Payload` is `map[string]any` with no schema, so any denylist is a list of what we happened to think of and is wrong the first time a shim adds a field. Precedent already in-tree: `TypeMailboxArrival` carries flags "never the message body". **Second, independent reason `minimal` must be the default:** the payload is not merely sensitive, it is **untrusted** — `POST /v1/internal/agent-event` is `accessTokenOwn` (`tokenauth.go:67`) and the payload passes through verbatim, so a prompt-injected agent can emit attacker-chosen text that lands in the operator's Slack looking like it came from Dejima. That is an injection channel *into* the trusted notification surface, riding a subscription the operator legitimately created; the owner-only bound on *creating* subscriptions does not cover payload *content*. Transport classification: **vendor list, unknown = external**, and derivation may only *restrict* — never upgrade a transport to "may send content" (a tailnet host is a public relay one config change later, and a URL cannot tell you that). Only an explicit per-subscription human choice permits `full`, and it should name which event types it applies to rather than being one forever-switch. Also wanted alongside: filter by agent and event type, quiet hours. (2-3 days)
- [ ] **Webhook URL is readable by any viewer** — *found 2026-08-18 while designing the above.* `GET /v1/events/subscriptions` is `capRead` (`roleauth.go:90`) and `Manager.List()` (`manager.go:190`) blanks `Secret` but returns `URL` in full. A Slack/Discord incoming-webhook URL **is a bearer credential** — anyone holding it can post to that channel from anywhere with no further auth — so a *viewer*-role member can lift the owner's webhook and post as it. Defensible while URLs are generic endpoints; **a precondition once Slack ships**, because then the URL is guaranteed to be a credential rather than possibly benign. Fix: mask for `capRead` (host + path prefix, e.g. `hooks.slack.com/services/T00…/***`), full value owner-only. Related to the HMAC work in *Webhook security hardening* above, but distinct — that hardens the *secret*, this exposes the *URL*. (hours)
- [x] **Headless-Mac service install via LaunchDaemon** — shipped in two tiers: `dejima service install` now falls back from the missing gui domain to `launchctl bootstrap user/<uid>` (supervised — KeepAlive crash restarts — for the current boot, no sudo, works over plain SSH), and `dejima service install --system` writes `/Library/LaunchDaemons/dev.dejima.dejimad.plist` (runs as the installing user via `UserName`; sudo for the privileged steps) which loads at boot with **no desktop login ever**. `restart`/`uninstall`/`status` honor `--system`. (done)
- [ ] **Headless boot vs locked login keychain** — a `--system` LaunchDaemon starts dejimad at boot *before any login*, when the user's login keychain is still locked, so keychain-sourced Claude creds can fail until someone SSHes/logs in once per boot. Fix: (a) make the daemon's creds probe detect the locked-keychain case and report it distinctly (doctor fix hint: "log in once, or seed file-based creds" — today it would misleadingly FAIL with "run `dejima auth push`" as if never logged in), (b) on `--system` installs, recommend/offer the file-based seed path (`dejima auth push`), which doesn't depend on the keychain. (half day)
- [ ] **Auto-login detection + recommendation** — during host provisioning, detect whether auto-login is enabled and recommend turning it on for headless Mac mini setups (so LaunchAgents survive reboots without needing a LaunchDaemon). (hours)
- [x] **Mac mini host setup runbook (`docs/mac-mini-host-setup.md`)** — Companion guide for people who'd rather read than be wizard'd: the six provisioning phases done by hand, the security model, and troubleshooting. Mirrors `onboard --provision-host`. (shipped)
- [x] **Interactive TUI (`dejima` with no args)** — bubbletea dashboard: live state, presence, keyboard nav, single-key lifecycle, a help overlay (`?`), an `n` repo-picker creator, a connection switcher (`s`), and open-in-new-window (`o`). One-shot CLI verbs still work for scripts. (done)
- [ ] **Default-on attach notifications at install** — `dejima service install --notify <url>` becomes the recommended path; first install prompts for a webhook URL. Awareness without surveillance. (hour)
- [~] **Audit log + read/export + viewer — the governance moat (pulled forward from v2).** The tamper-evident *Port-crossing* ledger is already shipped (hash-chained, host-side). This extends it to an **operational** audit log (`~/.dejima/ledger.jsonl`: API requests + lifecycle events, opt-in, optional HMAC) **and adds a read/export API + a basic viewer** — not just `dejima audit --verify`. **Core landed** (`feat/lane1-audit`): `dejimad --audit[/-reads/-hmac-key-file]` records `api.request` + curated lifecycle events on the shared chain; `GET /v1/audit` and `dejima audit` gain filters (island/type/actor/decision/since/until/limit) + `--export jsonl|csv` (whole-chain verification preserved); a TUI audit pane opens with `A`. Identity (who/role) on each record is consumed via an `AuditIdentity` context seam that Lane 2 populates; the team activity feed builds on this. Docs: [`audit.md`](audit.md). Remaining: identity enrichment + a live Minion run. **Decided 2026-06-19 that audit lives in Dejima, not the wrapper:** a tamper-evident record can't be delegated to a webhook-fed layer (engine-level placement required), which is why the crossing-ledger is already here. Compliance dashboards / multi-org rollups / retention-as-product stay above. This is the regulated-team wedge and is promised on the site's Teams page. (week+)
- [ ] **Opt-in trust-on-first-use** for new clients — paranoid mode for users who want stronger-than-tailnet auth. Off by default. (week)
- [ ] **Opt-in egress allow-list per island** — `network.allow = ["api.anthropic.com", ...]` in project config. Default: open. (day)

---

### Observability — real-time signals for dashboards / wrapper tooling

Dejima's stance: surface rich real-time state via the API and let wrapper tooling own history, aggregation, and per-user/per-org rollups (same division as the built-in-cost-tracking non-goal below). These are the real-time signals still worth exposing.

- [x] **Crash health in island detail** — `oom_killed`, `restart_count`, `exit_code` from `docker inspect`, surfaced on `GET /v1/islands/:name` and in the TUI detail pane. Signals an agent killed by its memory cap or a flapping container — facts a remote client can't observe itself. (done)
- [x] **Per-island disk usage** — workspace + home volume sizes surfaced alongside mem/cpu in `status` and the TUI detail pane. `runtime.VolumeSizes` does one `docker system df -v` call mapped by volume name; the daemon caches it 30s (slower than `docker stats`, disk drifts slowly) and only populates it on the detail endpoint (`IslandInfo.Disk`), never the list. Size reads 0 on storage drivers that don't report it (rendered only when > 0). (`internal/runtime/docker.go`, `internal/api`)
- [x] **Prometheus `/metrics` endpoint** — `GET /metrics` exposes islands-by-state, per-island cpu/mem/disk (workspace+home), restart/OOM counts, attached clients, panic state, and daemon build info, in Prometheus text-exposition format. Per-island series carry `island` + `owner` labels (per-team rollups via the ownership work). Hand-rolled (no client_golang dep, matching the docker-CLI ethos); reuses the cached stats/disk samples. Operator-level — the token-auth path default-denies it, so an island can't scrape the fleet. **`dejima_agent_idle_seconds{island,owner,agent}`** now ships too: seconds since each agent last emitted a state event (the agent-liveness heartbeat); only agents that have emitted appear (no misleading zero), and a clock-skew guard floors it at 0. Real-time only — history/aggregation stay in wrapper tooling. (`internal/api/metrics.go`)
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
  - [~] **Letta** first-class adapter — registry entry shipped (`feat/adapters-letta-hermes-goose`): headless `letta server`, self-installs via pip, launch sources the materialized provider key (`<PROVIDER>_API_KEY`), REST/UI on **8283** (`agent open` works). Live in-island install+launch check on Minion pending.
  - [~] **Hermes** first-class adapter — registry entry shipped: headless `hermes gateway`, key-env sourced; messaging bridge with no localhost UI → `GatewayPort` 0, injection-only. Install command + `hermes auth add` flow to confirm on Minion.
  - [~] **Goose** first-class adapter — registry entry shipped: headless `goosed`, self-installs via the official CLI installer, launch translates `DEJIMA_PROVIDER`/`DEJIMA_MODEL` → `GOOSE_PROVIDER`/`GOOSE_MODEL` + sources the key, web UI on **3000**. Live check on Minion pending.
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

Substantial engineering. **Exception:** the team-auth/roles + activity feed, audited MCP brokering, and SDK items in this section are **committed near-term** (see the build queue at top) — only the genuinely heavy rest (microVM, multi-tenant/RBAC, foreign containers, nested DinD, cross-host, NL control, web client, …) waits until the foundation is proven.

- [ ] **Conference rooms — many-agent conversation, with a secretary** *(requested 2026-09-03)* — `dejima msg` today is point-to-point plus a `(all)` broadcast: fine for dispatch, wrong for a discussion several agents need to hold *together*. A room is a named, addressable venue — agents join, the transcript is the room's, and a message lands once rather than N times. Two lifetimes, and the distinction is real rather than cosmetic: **fleeting** rooms are scoped to a piece of work and die with it (the design argument, the incident, the review), **persistent** rooms are standing venues that outlive any one task. A fleeting room that nobody remembers to close is a persistent room nobody meant to create, so closure needs an owner — see the secretary.
  - **The secretary/admin is the load-bearing part, not a nicety.** Context is the scarce resource in a room: without one, a room is a broadcast list that spends five agents' context on one agent's problem, and the bigger the fleet the worse the trade. Per-room duties: prune and compact the transcript, keep a running summary a joining agent can read instead of the backlog, and maintain the room's **key notes + decisions record** — what was decided, by whom, and what it closed. That last one is what makes a room worth more than a group chat.
  - **The decisions record should be Ledger entries, not a new store.** Dejima already has an append-only ledger with an activity feed over it; a room writing `room.decision` records inherits the audit properties rather than re-earning them. Same argument that kept folder-import inside Port (`docs/workspace-source.md`) — a second record of who-decided-what is a second thing to keep honest.
  - **Intra-island first, and cross-island only through the brokered grant** described under "Inter-agent + inter-island exchange" above. A room spanning islands is an ambient channel between them unless it is deny-all + scoped + ledgered, which is exactly the hole that section exists to not punch.
  - **Open questions.** Does the secretary have to be an agent (costs a seat, has judgment) or a daemon-side function (cheap, but summarising is a model job)? How does a room deliver — push into each member's mailbox, or pull on attach, given that unread state across N members is a harder problem than the 1:1 mailbox has already proven to be? And does a room have a *quorum* concept, or is "who is listening" simply whoever is joined? (open design, weeks)
- [ ] **Per-agent / per-island ACLs within a shared project** — when multiple islands share a workspace, define which agent can read/write which paths. Useful for delegated work streams ("frontend can write under /web, backend under /api, both read /shared"). Wrapper-product territory mostly; primitives may belong here. (open design, week+)
- [ ] **Trust-on-first-use for new clients** — unfamiliar attaches blocked until user approves via push notification on an already-trusted device. The 2FA-shaped feature. (week)
- [ ] **Token-based auth (single `owner` role)** — **committed (team rung — see build queue).** `dejima token create --label phone` issues a token; CLI/API consumers carry it via env or header. Doesn't replace Tailscale identity, complements it. Foundation for the wider roles model below. (week)
- [ ] **Three built-in roles + per-island scope** — **committed (team rung).** `owner` / `operator` (lifecycle but no purge) / `viewer` (read + observe). A token can be limited to specific islands. Lets wrapper products (Scusi, etc.) hold a service token with bounded power. (2 weeks)
- [x] **Activity feed** — **shipped (team rung — closes the build-queue #2 item).** "Who launched what, and which agent did what," across the team — a curated, owner-enriched, human-rendered timeline over the operational audit ledger. `GET /v1/activity` + `dejima activity` (filters: actor/island/owner/kind/decision/since/until/limit; `--json`). Classifies ledger entries into one item per action: `api.request` mutations → operator/human "who did what" (carrying Lane 2's authenticated actor+role), brokered `port/trade/capability/mcp` records → "which agent did what" to the host (always-on, so the feed works even without `--audit`), and `container.crashed`/`daemon.started` → system events; reads, redundant `island.*` lifecycle records, and telemetry are dropped. Viewer-readable (`capRead`), never reachable by an island token. The full who-did-what timeline needs `dejimad --audit` (the response carries an `audit_enabled` hint); the agent↔host broker slice shows regardless. (`internal/api/activity.go`)
- [ ] **Explicit auth non-goals (won't build inside Dejima)** — multi-tenant user UIs, OAuth/SSO, per-verb fine-grained ACLs, time-windowed tokens. Those belong in wrapper apps. Dejima ships **3 roles + island scope** and stops; anything richer is the wrapper's job. *(Same pattern as Postgres roles + Supabase auth.)*
- [ ] **(moved)** Operational audit ledger — consolidated into "Audit log + read/export + viewer" under v1.x (pulled forward; see above). It's the moat, so it's no longer a v2 deferral.
- [ ] **Backup / restore** — `dejima backup <name>` and `dejima restore` with a configurable destination (local path, S3, Backblaze, rsync target). User-configurable. (week)
- [ ] **microVM backend** — Firecracker/Apple Virtualization framework as an isolation upgrade. Real per-island VM rather than shared kernel. (weeks)
- [ ] **Audited MCP brokering** — **committed (build queue #3).** Deny-by-default grants of specific MCP (Model Context Protocol) servers into an island, declarative per-project, with **every call ledgered** — the Port/file-broker pattern applied to tools. MCP is now the default agent tool layer (Anthropic CMA and most platforms connect to it), so this is *parity* and a *differentiator* (nobody audits MCP access). (weeks)
- [ ] **Language SDKs (Python + TS) + OpenAPI spec** — **committed (build queue #4).** `pip install dejima-sdk` (and npm). Thin clients over the *existing* HTTP/WS API — they add ergonomics, not capability (the part they hide is the WebSocket PTY session stream + reconnection). Approach: publish an **OpenAPI spec** and generate the request/response client from it (so an API change is a regen, not hand-edits), then hand-write the small ergonomic layer (the PTY-stream helper). Ship now with a "0.x — may change" note; the CLI is already a Go client to mirror. Drops copy-paste snippets into the API docs for free. (week+ each)
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

- **Inter-island communication channels** — *under active design as of 2026-06-19* (see "Inter-agent + inter-island exchange" above). Was flatly out of scope; the revisit keeps it **brokered / scoped / audited only** (the Port pattern), never an open bus or ambient visibility — containment stays the default, exchange is an explicit, logged grant. *Unconstrained* cross-island channels (open message bus, RPC, ambient mutual visibility) remain out. Free-form multi-agent orchestration still belongs in wrapper apps over the public API.
- **Hosted/SaaS variant.** Dejima is OSS, self-hosted. No managed offering planned.
- **Windows host support** (running `dejimad` + Docker on Windows). The client works on Windows; the host doesn't. Out of scope.
- **Enterprise compliance certifications.** SOC 2 etc. are post-team-product, not v1/v2.
- **Built-in cost tracking for LLM API spend.** Out of scope; consume webhook events into your own dashboard.
- **Bundling LLM weights into Dejima itself.** The agents are the LLMs, and they live *inside* islands; a model baked into the daemon would bloat the binary, assume hardware (GPU/RAM), and go stale. Any natural-language / assistant features reuse credentials the user already has (see the optional NL control layer in v2) — Dejima ships no weights. Same principle as no-built-in-cost-tracking.
- **Real-time collaborative editing inside the workspace.** Sessions are shared-tmux, not shared-Cursor. Different problem.
