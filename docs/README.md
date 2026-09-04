# The docs, mapped

88 files, grouped by the question you arrived with. Every `.md` under `docs/` is
listed here exactly once, and `scripts/docs-index-check.py` fails the build if
that stops being true or if a link here points at a file that does not exist.

**Read this first, because it decides whether the rest is worth your time.**
This index solves DISCOVERY. Discovery has not been our problem. The five
knowledge failures of 2026-09-03 were each already documented, correctly, by
someone who knew — and three of the five were documented in the *same file* as
the code that got it wrong, one of them twenty lines above the edit. The reader
was not failing to find the rule. They had a specific correct belief that made
the general rule feel inapplicable.

So: a doc is the weakest instrument we have. The order that works is

> **enforce > co-locate > contextual pointer > central doc**

and the standing rule that follows from it is in the root `CLAUDE.md`: **a
lesson that recurs twice becomes a check, not a third comment.** If you are
about to write a fourth paragraph explaining something that keeps happening,
write a gate instead — `scripts/` has four of them and they work.

---

## "Something broke and every surface said it was fine"

The failure-shape library. Read these before diagnosing anything, and before
writing a guard.

- [testing/guards-need-controls.md](testing/guards-need-controls.md) — a guard
  whose failure mode is silence needs a control proving it can still see. Asks
  whether a check has a SUBJECT.
- [testing/readings-go-stale.md](testing/readings-go-stale.md) — a reading true
  when you took it looks exactly like one true now. Asks whether a reading is
  CURRENT. Includes the asymmetry that stale *caution* looks like diligence and
  survives review.
- [wsl-windows-postmortem.md](wsl-windows-postmortem.md) — the Windows/WSL host:
  what was broken and why nobody had seen it. Docker Desktop's VM was hiding two
  unrelated assumptions.
- [setup-failure-audit.md](setup-failure-audit.md) — where setup breaks and how
  the TUI/CLI should guide someone out.

## "What is this product and where is it going?"

- [positioning.md](positioning.md) · [roadmap.md](roadmap.md) ·
  [v1-spec.md](v1-spec.md) · [v1-milestones.md](v1-milestones.md)
- [security-boundary.md](security-boundary.md) — privilege exchange-down, the
  ironclad rule. Read before touching anything that crosses the island wall.
- [exit-ramp.md](exit-ramp.md) — `dejima eject`, the no-lock-in guarantee.
- [launch-checklist.md](launch-checklist.md)

## "How do I test this, and what does green actually mean?"

- [testing/coverage-gate.md](testing/coverage-gate.md) — how the suite stays
  fresh, and what counts as covering a command (an invocation; not a mention).
- [testing/automated-test-harness.md](testing/automated-test-harness.md) — the
  tier model: what runs on every PR vs. on a real host.
- [testing/full-suite-design.md](testing/full-suite-design.md) ·
  [testing/test-coverage-matrix.md](testing/test-coverage-matrix.md)
- [testing/dejimaqa-runner-setup.md](testing/dejimaqa-runner-setup.md)

## "How does someone install and run a host?"

- [mac-mini-host-setup.md](mac-mini-host-setup.md) — the reference host.
- [host-provisioning-plan.md](host-provisioning-plan.md) ·
  [distribution.md](distribution.md) — the channels and their state.
- [self-update.md](self-update.md) — `dejima update`, dual-mode.
- [npm-publishing-setup.md](npm-publishing-setup.md) ·
  [release-notarization.md](release-notarization.md) ·
  [metrics-install-totals.md](metrics-install-totals.md)
- [windows-native-daemon.md](windows-native-daemon.md) — research; the daemon
  does NOT build for native Windows, WSL2 is the only way.

## "How do agents work here?"

- [agent-adapters.md](agent-adapters.md) — the handler registry; how an agent
  type is declared rather than coded in.
- [custom-agents.md](custom-agents.md) ·
  [agent-adoption.md](agent-adoption.md) ·
  [ambient-agents-design.md](ambient-agents-design.md)
- [multi-agent-spec.md](multi-agent-spec.md) ·
  [multi-agent-impl-plan.md](multi-agent-impl-plan.md) ·
  [island-pid1-unification.md](island-pid1-unification.md)
- [local-models.md](local-models.md) — Ollama, the curated catalog, and what has
  to restart for an island to see a newly registered provider.
- [watch-view.md](watch-view.md) ·
  [runbook-openclaw-home-island.md](runbook-openclaw-home-island.md)
- [framework-backends.md](framework-backends.md) — Dejima as an SSH backend for
  Hermes/Goose/Remote-SSH.

## "How does anything cross the island wall?"

Deny-all is the default. Everything here is an explicit, audited exception.

- [port-island-spec.md](port-island-spec.md) — the Port: brokered host-file
  access. The design record every other crossing defers to.
- [folder-import.md](folder-import.md) ·
  [workspace-source.md](workspace-source.md) ·
  [paste-bridge.md](paste-bridge.md)
- [capability-broker-spec.md](capability-broker-spec.md) ·
  [capability-brokering.md](capability-brokering.md) ·
  [mcp-broker-spec.md](mcp-broker-spec.md)
- [secure-island-routing.md](secure-island-routing.md) ·
  [managed-island-files.md](managed-island-files.md) ·
  [host-terminals.md](host-terminals.md)

## "Who is allowed to do what, and what was recorded?"

- [secrets-manager-spec.md](secrets-manager-spec.md) ·
  [secrets-restart.md](secrets-restart.md) — a running process keeps its
  start-time environment; that is not a bug and it surprises everyone.
- [github-identities.md](github-identities.md) · [audit.md](audit.md)
- [teams/invite-format-spec.md](teams/invite-format-spec.md) ·
  [design/multi-tenant-ownership.md](design/multi-tenant-ownership.md)

## "How do agents and islands talk to each other?"

- [intra-island-coordination-spec.md](intra-island-coordination-spec.md) —
  within one island (the mailbox you are probably using).
- [inter-island-exchange-spec.md](inter-island-exchange-spec.md) — across
  islands: deny-all, granted, scoped, ledgered.
- [action-gate-spec.md](action-gate-spec.md) ·
  [action-gate-tui-client.md](action-gate-tui-client.md)
- [scheduled-wake-spec.md](scheduled-wake-spec.md) ·
  [scheduled-wake-design.md](scheduled-wake-design.md) ·
  [gateway-proxy.md](gateway-proxy.md)

## "Why does the terminal do that?"

- [tmux-passthrough.md](tmux-passthrough.md) — extended keys, CSI tails, and why
  binding a bare letter can eat an arrow key.
- [tui-recording-runbook.md](tui-recording-runbook.md) ·
  [drift-checker-design.md](drift-checker-design.md)

## Things a human has to run

These need a real host. They exist because the build island cannot do them.

- [operator-tests/release-acceptance.md](operator-tests/release-acceptance.md) —
  the human pass before a release.
- [operator-tests/clean-mac-launch-gate.md](operator-tests/clean-mac-launch-gate.md)
  · [operator-tests/inter-island-wave.md](operator-tests/inter-island-wave.md)
  · [operator-tests/uninstall-keep-islands-readopt.md](operator-tests/uninstall-keep-islands-readopt.md)
- Historical verify passes, kept for the record:
  [v0.6.1](operator-tests/v0.6.1-tui-verify.md) ·
  [v0.6.9](operator-tests/v0.6.9-verify.md) ·
  [v0.7.1](operator-tests/verify-v0.7.1.md) ·
  [v0.8](operator-tests/verify-v0.8.md) ·
  [v0.8.65](operator-tests/verify-v0.8.65.md)

## Work lanes

Parallel workstreams, so several agents can move without colliding. Start at
[lanes/README.md](lanes/README.md).

- [0.5-hardening](lanes/0.5-hardening.md) ·
  [0.5-onboarding](lanes/0.5-onboarding.md) ·
  [0.5-sdk-publish](lanes/0.5-sdk-publish.md) ·
  [0.6-completeness](lanes/0.6-completeness.md)
- [lane-0 verify harness](lanes/lane-0-verify-harness.md) ·
  [lane-1 audit](lanes/lane-1-audit.md) ·
  [lane-2 team auth](lanes/lane-2-team-auth.md) ·
  [lane-3 MCP](lanes/lane-3-mcp.md) ·
  [lane-4 SDK](lanes/lane-4-sdk.md)
- [lane-5 inter-island](lanes/lane-5-inter-island.md) ·
  [lane-5 phase 3.5 wake](lanes/lane-5-phase-3.5-wake.md)
- [lane-6 test harness](lanes/lane-6-test-harness.md) ·
  [lane-6 phase B](lanes/lane-6-phase-b.md) ·
  [lane-6 phase C](lanes/lane-6-phase-c.md)
- [skew detection](lanes/lane-skew-detection.md) ·
  [visual identity](lanes/lane-visual-identity.md)
