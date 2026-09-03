# Verification queue — v0.8.65

What still needs a human. Ordered by **risk**, not by feature: the top of this
list is code that has never been executed by anyone, anywhere.

Compiled 2026-08-16. Companion to
[`test-coverage-matrix.md`](../testing/test-coverage-matrix.md), which is the
system of record — but see *Blind spots* below, because three of the areas here
aren't in it at all.

## The clean-Mac gate is gone — nothing here is covered by it

**The `dejimaqa` runner was torn down (2026-08): it was crashing the operator's
real `dejimad`.** So `nightly-live.yml` has no host, and every `A†` row in matrix
§19 — the curl-pipe-sh, brew and npm install paths, the uninstall refusal,
`--keep-islands`, re-adopt — is **unproven and unrunnable**, not green. It never
earned its first virgin-Mac run.

Note a co-residency guard for exactly this failure *did* land (`443324e`,
`scripts/clean-mac/lib.sh` — refuse to run with a live daemon). The runner was
torn down after that, so the guard is either insufficient or the crash has a
different mechanism. **Diagnose that before rebuilding any QA host**, or the
next one takes the operator's daemon down again.

Consequence: fresh-Mac install is a **manual, high-priority** item (B1), not a
duplicate of a passing gate. It has been having problems in the field.

---

## Lane A — never executed by anyone

Highest risk in the tree. All of it shipped in the last four days, all of it is
Windows-side, and none of it can be exercised from a Linux island or from CI
(which only compiles Go). `docs/roadmap.md` already records that voice shipped
broken on Windows for exactly this reason.

- [ ] **A1 · `dejima wsl setup` end to end** — ~1000 lines (`cmd/dejima/wsl.go`
      + `internal/wsl/`) that have never run once. Expect: prompts before
      creating the distro and again before installing Docker; creates a
      *dedicated* distro named `dejima` without touching existing ones; installs
      socat + Docker + dejimad; starts the daemon; builds the island image;
      saves **and activates** a profile. Budget real time — an Ubuntu download
      plus an image build.
      **Watch for:** it switches your active profile on success
      (`wsl.go:483`). Two keystrokes to switch back, but it is not additive.
- [ ] **A2 · `dejima wsl status` / `start` / `stop`** against that distro.
- [ ] **A3 · Attach through `wsl://`** — open an agent over the socat tunnel and
      confirm the session behaves like a remote one (input, resize, detach).
- [ ] **A4 · TERM inference** — on Windows Terminal run `dejima doctor` and read
      the new **Terminal** section. Pass = `xterm-256color (inferred: WT_SESSION
      → Windows Terminal)` and *island rendering: full colour*. On legacy
      conhost, pass = the WARN branch, not a false OK.
- [ ] **A5 · `[w]` from the daemon-help panel** — point the TUI at `local` on
      Windows, wait for the unreachable panel, press `w`. Pass = a new window
      running `dejima wsl setup`, dashboard still alive behind it.
- [ ] **A6 · Truecolor actually reaches the agent** — the point of the whole
      TERM chain. Attach an agent and confirm full-fidelity colour returns.

## Lane B — the five areas

- [ ] **B1 · Fresh Mac mini install — the whole thing.** Known to be having
      problems in the field, and with `dejimaqa` gone there is no automation
      behind it at all.
      **Field failure #341, fixed 2026-08-18 (`f2119b4`) — verify this first.**
      `curl … | bash` leaves stdin a pipe, and the installer used `[[ -t 0 ]]`
      to decide whether anyone was present. On a fresh Mac mini it therefore
      installed Docker Desktop and Tailscale without asking, and skipped sudo
      pre-authorization — so Homebrew's own sudo appeared mid-cask as a bare
      `Password:`, took the password with echo on, and hung. Now resolved
      against `/dev/tty`. Pass = the one-liner asks before installing Docker,
      and the single password prompt arrives up front with a reason attached
      and is hidden. `scripts/lib/tty_test.sh` covers the decision; the rest of
      this item still needs a real Mac.
      Cover both halves:
      - the **install** itself, per channel (`curl | sh`, `brew`, `npm` client),
        on a genuinely clean box: daemon up, island image built, binaries on
        PATH, service registered under launchd, survives a reboot;
      - `uninstall` refuses by default, `--keep-islands` keeps the volume and
        `~/.dejima`, and a reinstall **re-adopts** the island by name;
      - the **first-run experience** — someone who has never seen Dejima runs the
        one-liner and reaches a working island without reading docs. Note every
        point they hesitate; that half was never automated even when the gate ran.
- [ ] **B2 · Local install, macOS** — `install.sh` → daemon under launchd →
      `local` in the switcher is a real target → `dejima init` → attach. Overlaps
      B1's install half; the distinct part is that the *switcher's* `local` entry
      behaves once a daemon actually exists on the box.
- [ ] **B3 · Local install, Windows** — **not in the matrix at all.** Only
      possible at all as of v0.8.64 (WSL2). Overlaps Lane A but is the
      user-facing framing: can a Windows user get isolated local islands?
- [ ] **B4 · Cloud install** — **not in the matrix at all.** No section covers a
      Linux VPS: install, `--tcp` exposure, token auth, Tailscale-or-not,
      reaching it from a laptop client. Given `69a8e89` (a tailnet that isn't up
      must not kill dejimad) this path is being exercised in production without
      being tested.
- [ ] **B5 · OpenClaw — KNOWN BROKEN.** Not untested: it has been exercised and
      does not work. So the task is *reproduce, diagnose, fix*, then verify —
      not "run the checklist and see". Capture the actual failure first (which
      step, what error, which log) before touching code. Surface to cover once
      it runs: `home create --agent openclaw` self-installs and idles without
      crash-looping; `--bind loopback` launch; the home-role attachability gate;
      `agent open` reaching its console — and note C4, since that path has a
      known misdiagnosis that will send you the wrong way.
- [ ] **B6 · Secrets management — KNOWN BROKEN.** Also exercised, also not
      working. Reproduce and capture the failure before changing anything;
      `dejima secret` has **no matrix row at all** (now §20), so there is no
      prior expectation written down to check against — establish what correct
      looks like as part of the fix. Surface: `set` (prompts, never echoes),
      `ls` (names only), `rm`; the value reaching the agent *and* its tool
      subprocesses via the non-login-shell path; a secret added after a shell
      started being absent until a new one; a restarted agent picking it up;
      keychain rather than plaintext.

## Lane C — shipped this week, thinly verified

- [ ] **C1 · Upgrade resume** (`a0bd706`) — partially proven live (all four
      agents returned on the 2026-08-12 upgrade). Still unproven: a *hibernated*
      island upgraded stays stopped and doesn't try to reconcile; and an island
      whose agents have no prior transcript comes back cold without a dead tmux.
- [ ] **C2 · Stale-reply guard** (`42d4eef`) — switch targets in the TUI while a
      poll is in flight; the previous server's islands must vanish immediately
      and the unreachable diagnosis must stay on screen. This is the bug that
      hid the `[w]` offer, so verify A5 *after* this.
- [ ] **C3 · Doctor on a client** — from Windows or any box pointed at a remote
      daemon: `docker` and `island image` report as daemon-host facts, and
      `dejima doctor` exits **0** on a healthy client.
- [ ] **C4 · `agent open` diagnosis** — unenrolled SSH must not be reported as a
      missing pinned token, and the remedy must never be `dejima upgrade`, which
      restarts every agent in the island to fix something it cannot fix.
- [ ] **C5 · Connection delete confirmation** (`38cb475`) — `d` stages, only `y`
      commits, Enter is inert, `local` undeletable.
- [ ] **C6 · Drag-drop paste** (`317edb5`) — a dropped file uploads rather than
      typing a local path.

## Blind spots in the matrix itself

Three of the areas above return **zero** hits in `test-coverage-matrix.md`:
`dejima secret`, Windows anything, and cloud/Linux hosting. They aren't tracked
as untested — they aren't tracked at all, so a rollup reading "green" is silent
about them. Worth adding sections rather than only working through this file.

## Standing constraint

Everything in Lane A is untestable from the dev box and from CI. That has
produced a shipped-broken Windows feature once already. Until a Windows box runs
it, treat A1–A6 as **unknown**, not as "probably fine because it compiles".
