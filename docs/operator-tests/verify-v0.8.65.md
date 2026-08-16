# Verification queue — v0.8.65

What still needs a human. Ordered by **risk**, not by feature: the top of this
list is code that has never been executed by anyone, anywhere.

Compiled 2026-08-16. Companion to
[`test-coverage-matrix.md`](../testing/test-coverage-matrix.md), which is the
system of record — but see *Blind spots* below, because three of the areas here
aren't in it at all.

## First: what NOT to re-test

A fresh-Mac install is **already automated** and runs nightly. `nightly-live.yml`
drives a real Mac mini as the `dejimaqa` user through the clean-Mac launch gate,
and matrix §19 marks the curl-pipe-sh, brew --HEAD and npm client paths, the
uninstall refusal, and `--keep-islands` as `A†` (proved on a genuinely virgin
host). Re-running that by hand duplicates a green gate.

What that gate does **not** prove is the *first-run experience*: whether the
onboarding wizard reads well, picks sane defaults, and leaves someone who has
never seen Dejima with a working island. That's Lane B1.

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

- [ ] **B1 · Fresh Mac mini, first-run UX** *(not the install itself — see
      above)*. A person who has never used Dejima runs the one-liner and gets to
      a working island without reading docs. Note every point they hesitate.
- [ ] **B2 · Local install, macOS** — `install.sh` → daemon under launchd →
      `local` in the switcher is a real target → `dejima init` → attach. Largely
      covered by the gate; what's untested is that the *switcher's* `local` entry
      behaves once a daemon exists.
- [ ] **B3 · Local install, Windows** — **not in the matrix at all.** Only
      possible at all as of v0.8.64 (WSL2). Overlaps Lane A but is the
      user-facing framing: can a Windows user get isolated local islands?
- [ ] **B4 · Cloud install** — **not in the matrix at all.** No section covers a
      Linux VPS: install, `--tcp` exposure, token auth, Tailscale-or-not,
      reaching it from a laptop client. Given `69a8e89` (a tailnet that isn't up
      must not kill dejimad) this path is being exercised in production without
      being tested.
- [ ] **B5 · OpenClaw** — matrix §16, every row unchecked and marked `M`:
      `home create --agent openclaw` self-installs and idles without
      crash-looping; `--bind loopback` launch; the home-role attachability gate;
      and `agent open` reaching its console (see C4 — that path has a known
      misdiagnosis).
- [ ] **B6 · Secrets management** — **not in the matrix at all.** `dejima secret
      set` (prompts, never echoes), `ls` (names only, values never shown), `rm`.
      Then the part that actually matters: a secret set *after* an agent's shell
      started is **not** in that shell's environment until a new one — verify the
      documented behaviour, and that a restarted agent does pick it up.
      Also: values land in the OS keychain, not plaintext config.

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
