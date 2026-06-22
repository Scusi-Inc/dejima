# Operator release-acceptance smoke (the human pass)

The short list **you** run before tagging a release. This is *not* the exhaustive list —
that's [`test-coverage-matrix.md`](../testing/test-coverage-matrix.md) (~150 items, the
target for automation). This is the curated "if these core journeys work, ship it" pass
plus the handful of things a person should eyeball. ~15–20 min on Minion.

As Lane 6 + the Mac-mini runner drive matrix rows to `A` (automated), most of this becomes
belt-and-suspenders — but the **go/no-go** and the real-world/subjective checks stay human.

## Core journeys (each: do → expect)
- [ ] **Create** an island from a repo → `dejima ls` shows it running; workspace ready.
- [ ] **Add an agent** (`dejima agent add`) → it launches; `dejima agent open` reaches its UI.
- [ ] **Terminal** — attach, run a command, get output. Then **drop the link** (restart the
  daemon, or sleep/wake the Mac) → the terminal **reconnects, doesn't close**. `Ctrl-b d`
  detaches cleanly without killing the agent.
- [ ] **Real work + git** — let the agent change a file; `dejima push` works; `dejima purge`
  **warns on uncommitted/unpushed work** and the confirm makes you **type the island name**.
- [ ] **Hibernate → wake** an island → state intact.
- [ ] **Port** — `port grant` a host dir, `port intake` a file (incl. a `chmod 600` one) →
  readable in-island; a `../` traversal is refused.
- [ ] **Audit** — `dejima audit` shows the above actions; `audit --verify` passes;
  `dejima activity` shows who+which-agent did what.
- [ ] **Team/roles** — a `viewer` token can read but is **denied `purge`**; an out-of-scope
  island is denied.
- [ ] **Inter-island** — run [`inter-island-wave.md`](inter-island-wave.md)
  (deny-all → grant → message → action approve/deny → wake-on-message).
- [ ] **Onboarding/service** — `service install --system --audit` (or `onboard
  --provision-host`) brings the daemon up audited; **reboot → still reachable, no login**.
- [ ] **Keychain + idle-hibernate** — a webhook secret is **not plaintext** in config;
  idle auto-hibernate fires and the island wakes on use.
- [ ] **Update** — `dejima update` (or TUI `U`) moves client + daemon to the new release.
- [ ] **TUI eyeball** — list/menu render; the confirm pop-up shows the name-to-type;
  the `m` action menu deletes an agent/island **without stalling**.
- [ ] **Purge** the test islands cleanly.

## Stays human-only (never fully automated)
- The **go/no-go** call to tag the release.
- **Real-world** sleep/wake, lid-close, network-blip behavior (automation simulates; you
  confirm on real hardware now and then).
- **Subjective UX** — does it feel right.
- **Flaky real-agent** behavior — automation smokes it; you spot-check.

## Pass → tag
All core journeys green + nothing alarming in the eyeball → cut the release
(`git tag -a vX.Y.Z <sha>` by SHA; see the version map in `roadmap.md`).
