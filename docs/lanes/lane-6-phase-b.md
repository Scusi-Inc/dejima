# Lane 6 — Phase B (live tiers: macOS runner + Tier-3 suite)

Phase A automated the no-account tiers (T1 CI + T2 matrix authored). **Phase B wires the
live nightly run on the Mac-mini self-hosted runner and adds the Tier-3 macOS suite** — the
part that retires most of the manual operator pass.

**Prereqs (operator, see `docs/testing/dejimaqa-runner-setup.md`):** the `dejimaqa` runner is
live with **colima Docker**, a **bot GitHub account** + `TEST_GH_TOKEN`/`TEST_GH_OWNER`, and a
**Claude key** `TEST_AGENT_KEY` as repo secrets. Don't assume these at author time — gate the
workflow so missing secrets **skip** (not fail) the dependent jobs.

**Read first:** `docs/testing/test-coverage-matrix.md` (the rows to flip `T2`/`T3`/`T4` →
`A`), `docs/testing/automated-test-harness.md`, `docs/operator-tests/inter-island-wave.md` and
`release-acceptance.md` (the human checks you're automating), `scripts/integration.sh` (your
Phase-A Tier-2 base), the Phase-A nightly-workflow stub.

## Scope (each its own PR)

1. **Nightly workflow** `.github/workflows/nightly-live.yml`:
   - Triggers: `schedule` (nightly cron), `workflow_dispatch`, and a `live` PR label — **never**
     on every push/PR; **never** on fork PRs (guard with the actor/label check).
   - `runs-on: [self-hosted, macOS, macos-mini]`.
   - Inject `TEST_GH_TOKEN` / `TEST_AGENT_KEY` / `TEST_GH_OWNER`; jobs needing a missing secret
     **skip with a notice**, don't fail.
   - *(Pushing `.github/workflows` needs the gh `workflow` token scope — see that note.)*
2. **Tier-2 on the runner:** run the Phase-A `integration.sh` matrix against `dejimaqa`'s
   colima Docker (lifecycle / Port / MCP / audit / inter-island). Disposable island namespace;
   purge + revoke on teardown; never touch real projects.
3. **Tier-3 macOS suite (new scripts):**
   - `service install --system --audit` (scoped sudo) → daemon up + audited; **reboot-survival**
     check (reachable, no login) — gate the actual reboot behind `workflow_dispatch` only.
   - `onboard --provision-host` in **idempotent/dry-run** mode (don't re-install packages nightly).
   - **Keychain** secret storage (webhook secret not plaintext in config).
   - **idle auto-hibernate** fires + wakes.
   - **terminal auto-reconnect** (drop the link mid-session → reattaches).
   - **per-adapter wake** (the P3.5 live unknown): does Claude Code wake from the tmux
     `send-keys` nudge and from hibernation, without clobbering a busy agent? If `send-keys` is
     rough, that's the signal to swap the inject seam for a hook adapter.
   - SSH-façade: shell/sftp/VS Code land in `/workspace`.
4. **Tier-4 agent smoke (behind `TEST_AGENT_KEY`):** create an island from a bot sandbox repo,
   launch a real agent, trivial task, `push` to the bot repo, exercise one inter-island
   message+action. **Lenient** — assert "ran / produced a commit," not exact output.
5. **Reporting:** each green run flips the matching `test-coverage-matrix.md` rows `Now` → `A`;
   emit a summary; fail the job on regressions (not on the lenient T4 smoke).

**Owns:** `.github/workflows/nightly-live.yml`, new `scripts/` Tier-3 test scripts + helpers.
**Don't:** change product behavior; run as anything but `dejimaqa`; assume secrets exist; run
on fork PRs; leave islands/tokens behind (idempotent teardown).

**Verify:** a manual `workflow_dispatch` run on `macos-mini` goes green (or cleanly skips
secret-gated jobs); matrix rows flip to `A`. Report which rows automated + any live failures
(especially per-adapter wake) back to the orchestrator.
