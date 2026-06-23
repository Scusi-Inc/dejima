# Setup failure-point audit — where setup breaks and how the TUI/CLI should guide the user out

**Status:** audit + design direction. First code installment shipped in PR #63
(local daemon-unreachable diagnosis). The rest below is prioritized, not yet built.

**Why this exists:** "dead-simple, up in 5 minutes" is the launch promise. Today the
tooling gets a user ~90% of the way and then, at the last mile, either **declares
success when nothing is running** or **dead-ends on a raw error with no next step**.
This document maps every setup failure point we found across onboarding, provisioning,
`doctor`, service install, Docker/colima, Tailscale, and credentials — and states the
guidance rule each one should follow.

It was written after the first live `dejimaqa` nightly run (2026-06-23), which surfaced
two of these patterns in practice (see [Appendix: first-run findings](#appendix-first-live-run-findings)).

---

## The five cross-cutting patterns

Every individual finding is an instance of one of these. Fix the pattern, not just the
instance.

### P1 — "Complete" must mean "working", never "got through the steps"
The onboarding and `--provision-host` wizards collect per-step errors into a "manual
checklist" and then return success (`✅ Provisioning complete`) **even when `dejimad`
was never installed or started** (`provision.go` phases 5–6 return `nil` on failure;
`onboard.go` lets the user decline `make setup` and finish anyway). Worse, finishing
writes the dismissal marker (`~/.dejima/onboarding-dismissed`), so the adaptive first-run
prompt **never offers to help again** — the user is stranded with a cached half-setup.

> **Rule:** a setup flow may only print success *after* it has reached the daemon
> (`client.Health()` succeeds). If health fails at the end, it ends in the FAILED state,
> names the one blocking step, and does **not** write the dismissal marker. The marker
> should mean "setup verified working once," not "wizard exited."

### P2 — Every terminal error classifies the cause and offers a command
A raw transport/exec error is never the last word. PR #63 is the template: probe the
real cause (socket missing vs. refused vs. perm-denied vs. installed-but-not-loaded) and
print ordered, copy-pasteable fixes. The same treatment is owed to Docker-not-reachable,
in-island git-auth failures, and image-missing.

> **Rule:** any error the user can hit at a prompt or in the TUI status line must answer
> "what's wrong" + "the exact command to fix it" + "`dejima doctor` for more."

### P3 — A failed prerequisite stops its dependents; surface the root cause, not the symptom
`brew`/`docker`/`tailscale` install failures are treated as "optional" warnings the
wizard continues past. Later phases then fail with confusing downstream errors
(`command not found`, "docker not reachable") whose root cause (brew failed three phases
ago) has scrolled off screen.

> **Rule:** a phase that depends on a tool checks that tool first and, if its install
> failed earlier, refuses with a message that points back at the *root* failure — not
> the symptom.

### P4 — Check credentials/keys before the user commits, not after
The TUI lets a user create an island with no Claude credentials and no LLM provider key;
both fail **silently at first agent attach**, long after the create. CLI `dejima auth
status` warns, but nothing in the TUI create-flow or empty-state does.

> **Rule:** the empty-state and the create flow surface missing Claude creds / provider
> keys up front, with the one-line fix (`dejima auth push`, press `v` to set a key), so
> the failure is caught before an island exists rather than at runtime.

### P5 — `doctor` is the de-facto repair tool; make it reachable and close its blind spots
`doctor` already has strong, command-bearing remedies and three `--fix` repairs (VM
resize, profile port, install-meta). But it is **only reached from error paths when
`DEJIMA_HOST` is set** (local-socket failures didn't point at it until PR #63), and it
has blind spots that map exactly to the common real failures.

> **Rule:** every FAIL path names `dejima doctor`; doctor grows the missing checks below;
> `--fix` covers every remedy that's safely automatable.

---

## Prioritized findings

Severity = impact on a non-expert doing first-time setup. "Pattern" links to the rule above.

### P0 — breaks setup silently or strands the user (do first)

| # | Failure point | Today | Pattern | Fix direction |
|---|---|---|---|---|
| 1 | `--provision-host` finishes `✅ complete` though `dejimad` never installed (no source checkout, or `service install`/`make setup` failed) — `provision.go:473-508` returns `nil` | Wizard prints success + "connect from other devices"; daemon down | P1 | Final phase gates on `client.Health()`; on failure → FAILED state naming the blocking step; don't dismiss |
| 2 | Dismissal marker written on incomplete setup → adaptive prompt never returns (`onboard.go:54`) | User can't re-trigger guided setup; stuck on raw "daemon unreachable" | P1 | Write marker only after a verified-healthy daemon; otherwise re-offer on next run |
| 3 | Generic onboard lets user decline `make setup` (default **No**) and continue (`onboard.go:464-471`) | Finishes with `✗ daemon: nothing running`, no "go back" path | P1 | If declined, end in a clearly-unfinished state with the exact resume command |
| 4 | Local "daemon unreachable" gave only the raw dial error in CLI + TUI | — | P2 | **Shipped in PR #63** — socket/service probe → classified fixes |

### P1 — common real failures with poor or missing guidance

| # | Failure point | Today | Pattern | Fix direction |
|---|---|---|---|---|
| 5 | **colima stopped** is indistinguishable from "Docker not installed" — both just "not reachable" (`doctor.go:297-305`, `dockerReachable()`) | User told to *install* Docker when it's installed-but-stopped | P2/P5 | doctor probes `colima status`; if a stopped VM is found → "start it: `colima start`" instead of the install link |
| 6 | Docker-not-reachable points at a **download URL**, not a runnable next step (`doctor.go:300`) | Novice can't tell "not installed" from "not started" from "socket perms" | P2 | Classify like PR #63: missing → install; present-but-down → start; socket perm (Linux) → add to `docker` group |
| 7 | Brew/Docker/Tailscale install failures in provision are non-fatal; dependents fail later with opaque errors (`provision.go:272-378`) | Cascading "command not found" with root cause off-screen | P3 | Gate each dependent phase on its tool; on prior failure, refuse pointing at the root |
| 8 | First island needs Docker **running** before `make setup`, but the step list doesn't say so (`onboard.go:418-456`) | `make setup` fails with vague build errors | P2/P3 | Mark Docker a hard prerequisite of the install step with an inline check |
| 9 | Undersized Docker VM (OOM #23) | Well-handled: warned in onboard/doctor/TUI; `doctor --fix` auto-resizes colima | — | Already good; Docker-Desktop path remains GUI-only (can't CLI-resize) |

### P2 — last-mile credential/runtime gaps (fail after setup "succeeds")

| # | Failure point | Today | Pattern | Fix direction |
|---|---|---|---|---|
| 10 | No Claude creds → island starts, agent fails silently at attach (`server.go` credentialBindMounts skips silently) | Only `dejima auth status` warns; TUI silent | P4 | TUI empty-state + create flow check creds; warn with `dejima auth push` before create |
| 11 | Key-requiring agent with no provider key | Visible at runtime (`⚠ no model key`, press `v`); not before create | P4 | Surface key requirement during agent pick in the create flow, not only after |
| 12 | In-island git clone/push auth failure → raw `fatal: Authentication failed` | No Dejima-level guidance; token freshness never re-checked | P2 | Wrap git-auth failures with "check/refresh the identity: `dejima auth status` / `dejima auth push --github`" |
| 13 | Private-repo URL entered directly skips the GitHub-identity hint (only shown when browsing) (`tui_create.go:454-465`) | Clone fails inside island with no upfront warning | P4 | Detect private/SSH repo at create; if no daemon identity, warn before provisioning |

### P3 — durability / discoverability polish

| # | Failure point | Today | Pattern | Fix direction |
|---|---|---|---|---|
| 14 | launchd "gui" or "user" domain on a headless Mac won't survive reboot | `detect.go`/doctor WARN with the exact `--system` fix | — | Already good — keep |
| 15 | systemd `--user` without linger won't start until next login | doctor WARN with `loginctl enable-linger` | — | Already good — keep |
| 16 | First island triggers a multi-minute image build with no progress/ETA (`tui_create.go:730`) | "provisioning…" with no hint it's a long build | P2 | "building island image (first time, ~N min)…" with progress |
| 17 | Zero-to-host steps before onboard can reach the box (create account + password, enable Remote Login/SSH, get IP, Tailscale login) are mostly manual; headless Mac can't do Tailscale's browser login in-place | Partially handled inside `--provision-host` phases 1–4, but only if you reach them | P1 | Client-side "no host yet?" path that walks the pre-SSH steps; document the headless Tailscale `--auth-key` route |

---

## What `doctor` is missing (the P5 blind spots)

Add these checks; each maps to a real first-setup failure not currently diagnosed:

- **colima stopped vs. absent** (finding 5) — the single most common "Docker not reachable."
- **Docker socket permission** (Linux: user not in `docker` group) — `docker version` fails with a perms error we can recognize.
- **Port already in use** — `:7273` / `:7274` held by another process makes the daemon fail to start with no current diagnosis.
- **`~/.dejima` writability / ownership at first run** — partially covered (`checkStateOwnership` finds root-owned files post-hoc); add a pre-flight writability check.
- **Daemon installed-but-not-loaded as a standalone check** — today only inferred via supervision; make it a first-class daemon-down cause (PR #63 does this for the client side).

And make doctor **reachable**: every FAIL message ends with `dejima doctor`. (PR #63 already routes local daemon failures toward it.)

---

## Suggested sequence

1. **P0 #1–3** — wizard verifies health before success + correct marker semantics. This is
   the highest-leverage change: it converts the worst failure (silent half-setup that can't
   be re-triggered) into a clear, resumable state. Small, contained, no new surface.
2. **PR #63** (done) — local daemon-unreachable diagnosis.
3. **#5–6** — Docker/colima classification in doctor (+ the new doctor checks). Same
   PR #63 pattern, applied to the other thing every first-timer hits.
4. **#10–11** — credential/key pre-checks in the TUI create flow and empty-state.
5. **#7–8, #12–13, #16–17** — prerequisite gating, git-auth wrapping, build progress,
   zero-to-host walkthrough.

---

## Appendix: first live run findings

First live `nightly.yml` run on the `dejimaqa`/`macos-mini` self-hosted runner
([run 28042814558](https://github.com/aoos/dejima/actions/runs/28042814558), 2026-06-23):

| Job | Result | Note |
|---|---|---|
| tier3-macos-host-safe | ✅ pass | macOS host-safe checks green |
| tier4-agent-smoke | ✅ pass | real agent launched — `TEST_AGENT_KEY` works end to end |
| tier3-macos-host-system / c2-onboard-selftest | ⏭ skipped | opt-in, correctly gated off |
| tier2-integration | ❌ red | **the suite itself PASSED**; the job only failed because `scripts/report-to-issue.sh` died with a shell-quoting bug (`line 94: unexpected EOF while looking for matching '`) — a false failure from our own reporting script |
| c2-tui-claude | ❌ fail | Claude screen-analysis flagged that fixture islands `tui-alpha`/`tui-beta` weren't present (`no islands yet`), failing the 4 island-dependent screens; help-overlay + audit-pane passed. Needs triage: fixture seeding vs. stale expectation |

Two concrete actions fall out of the run (separate from this audit's roadmap):

1. **Fix `scripts/report-to-issue.sh`** — a quoting/heredoc bug both (a) turns a green
   suite red and (b) breaks the "report failures back as a GitHub issue" channel. No
   failure issue was filed for *either* failing job this run, confirming the channel is
   down. High priority — it's the feedback loop the whole harness depends on.
2. **Triage `c2-tui-claude` fixture** — determine whether the two expected islands failed
   to seed on the runner (environmental) or the expectation is stale (test bug).
