# `dejimaqa` — fully-capable-tester setup (Minion)

Everything the `dejimaqa` account on the Minion Mac mini needs to run the **full** live
test suite (Tier-2 Docker + Tier-3 macOS + Tier-4 agent). Isolation principle: `dejimaqa`
runs **its own** `dejimad` + Docker (own state, own islands), so it never touches `aoos`'s
real islands; all its git/agent activity goes to a **throwaway bot account**, never `aoos`.

Legend: ✅ done · ⏳ to do (operator).

## Identity & privilege
- ✅ Non-admin account `dejimaqa` (uid 503), password set, home created.
- ✅ Scoped passwordless sudo (`/etc/sudoers.d/dejimaqa`): `dejima`, `pmset`, `systemsetup`,
  `launchctl` only — enough for `service install --system` / `onboard` without admin.
- ✅ GitHub Actions self-hosted runner `minion-qa`, label `macos-mini`, registered to
  `aoos/dejima`, **caged**: repo workflow token is **read-only** (Part 1), runs as `dejimaqa`.

## Docker (for island / Tier-2 / Tier-3 tests)
- ⏳ Use **colima** (headless Docker for macOS — CLI, no GUI session, works over SSH), *not*
  Docker Desktop (which is per-user GUI and would need auto-login). Minion already uses colima
  for `aoos`, so the binaries exist; `dejimaqa` runs its **own** colima VM:
  ```bash
  # binaries (once, as aoos if not already): brew install colima docker
  # as dejimaqa:
  colima start --cpu 4 --memory 8 --disk 60      # its own VM, separate from aoos's
  docker ps                                       # confirm the daemon answers
  ```
  - **RAM note (#23):** two colima VMs (aoos + dejimaqa) coexist — make sure the mini has the
    memory, or stop `aoos`'s colima during nightly runs. Right-size with `dejima doctor --fix`.
  - If `dejimaqa` can't run `brew install` (Homebrew prefix is owned by `aoos`): install the
    binaries once **as `aoos`** (shared in `/opt/homebrew`), then `dejimaqa` only does
    `colima start` (its own VM) — no brew-write needed.
- ⏳ `dejimaqa` runs **its own `dejimad`** (own `~/.dejima`, pointed at its colima) for test
  isolation. The `service install --system` test is the one scoped-sudo exception.

## Bot GitHub account (island sources + push targets)
- ⏳ Create a throwaway **bot account** (fresh email; gmail `+alias` ok) — e.g. `dejima-qa-bot`.
- ⏳ Add 2–3 **sandbox repos** (import small OSS projects) — islands are created from / pushed to these.
- ⏳ A **fine-grained PAT** (bot account): repository access = only those sandbox repos,
  Contents: read+write. This is `TEST_GH_TOKEN`.
- For manual `dejimaqa` runs: `export GH_TOKEN=<bot PAT>` in its shell (don't use `aoos`'s gh).

## Agent credential (Tier-4)
- ⏳ A throwaway/cheap **Claude API key** → `TEST_AGENT_KEY`. Used to launch real agents
  (Claude Code / OpenClaw) for the agent smoke. Inject via env or `dejima provider set`.

## Repo secrets / vars (add as `aoos`, consumed by the nightly workflow)
- ⏳ `gh secret set TEST_GH_TOKEN --repo aoos/dejima`   (bot PAT)
- ⏳ `gh secret set TEST_AGENT_KEY --repo aoos/dejima`  (Claude key)
- ⏳ `gh variable set TEST_GH_OWNER --repo aoos/dejima --body dejima-qa-bot`

## Runner persistence (optional; for unattended nightly)
- ⏳ FileVault is **off**, so auto-login is possible — but `sysadminctl -autologin` returned
  `error:22` (likely the password's special chars). Fallback: set it via the manual
  `kcpassword` method, or just run the runner in **tmux** (`tmux new -s runner; ./run.sh`),
  which survives SSH disconnects (not reboots). Decide when you have physical access.

## What stays out of `dejimaqa`'s reach (by design)
No admin; sudo limited to 4 binaries; the repo job-token is read-only; all git/agent writes
go to the bot account. A compromised test run can't touch `aoos`'s account, repos, or islands.

## Mutating-test guardrail
The `onboard --provision-host` / `service install` tests run in **idempotent / dry-run** mode
in CI so nightly runs don't re-install system packages or thrash the box. A full from-scratch
provision is an occasional **manual** run, not nightly.
