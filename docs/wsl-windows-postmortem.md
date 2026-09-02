# The Windows/WSL host: what was broken and why nobody had seen it

**Status:** the path works. A wiped Windows machine — WSL uninstalled, its
features disabled, no Dejima — reached a running island with a private repo
cloned, on 2026-09-02 at v0.8.96, with no manual steps. That had never happened
before. One blocker remains (socket exhaustion, below) and it is not an install
problem.

This is the reference for what was wrong, because ten separate defects turned
out to be about three things, and the three things will recur.

## The one sentence

**Every machine we had ever tested on put a VM between the container and the
host, and two unrelated subsystems silently depended on it.**

Docker Desktop and colima run containers inside a VM. That VM maps uids and
forwards `host.docker.internal` to the host's loopback. WSL installs a NATIVE
engine — `installDocker` uses get.docker.com — so there is no VM and no mapping,
and both assumptions broke at once:

| Assumption | Where it lived | What broke |
| --- | --- | --- |
| the container can read a root-owned file | `islandGHConfigDir` writes 0600 in a 0700 dir | island could not read its own gh credential |
| the container can reach the host's loopback | egress proxy and token listener bind 127.0.0.1 | `git clone` could not reach the egress proxy |

Neither is a WSL bug. Both are latent on **any** native Docker engine, including
plain Linux hosts, and were invisible because every host we used had a VM.

## The second pattern

**The unattended context lacks what every interactive context supplies for
free.**

Five failures, found one at a time, each only after the previous was fixed:

1. `nohup dejimad &` — died with the wsl.exe session
2. `setsid nohup … </dev/null &` — died too; WSL tears a distro down with its
   last interop session, so nothing detached inside it can outlive the command
   that started it
3. wsl.conf boot command, bare name — `setsid: failed to execute dejimad: No
   such file or directory`; the boot PATH excludes `/usr/local/bin`
4. systemd unit, no `HOME` — `locate home dir: $HOME is not defined`, restarting
   every two seconds forever
5. a stale socket — `dejimad` refuses to bind over one

`wsl -d dejima -- dejimad` has PATH and HOME and works every single time. That
is why none of this was findable by hand: **hand-testing is precisely the
environment that hides it.**

The answer was supervision from the distro's own init (a systemd unit), not
better detaching. Every line of that unit earned its place by failing first —
see `cmd/dejima/wslservice.go`, which records why each one is there.

## The third pattern

**A report of actions taken is not a statement about the resulting state.**

`dejima wsl setup` printed a complete, truthful list — socat present, Docker
present, dejimad running, image built, connection verified — and had silently
skipped installing the systemd unit, because that step sat below an early
return that fires when the daemon is already up. It ran three times, reported
clean three times, changed nothing.

**A skipped step prints nothing, and nothing is indistinguishable from the gap
between two successful lines.** The fix was ordering (`c13fd22`); the lesson is
that setup should prove the end state rather than narrate the steps.

## The defects, with their commits

| # | Defect | Fix |
| --- | --- | --- |
| 1 | client installed into root-owned `/usr/local/bin` could never self-update | `076e6f0` |
| 2 | `DEJIMA_HOST` written to a file the user's shell never reads (rc chosen by OS, not shell) | `f7c58cc` |
| 3 | island creation ran a Docker build that could not succeed, and reported a socket path from another machine | `cd62473` |
| 4 | `dejima doctor` declined to check the host's Docker — "runs on the daemon host, not here" | `5f2294f` |
| 5 | "socat isn't installed" reported about an installed socat (classifier matched socat's OWN error) | `ad2aa8e` |
| 6 | failed daemon restart printed macOS advice on a Linux host | `f03eae8` |
| 7 | daemon did not survive its distro restarting | `075ab2c` |
| 8 | every `wsl.exe` spawn shared the operator's console — arrow keys stopped working, UP opened the audit ledger | `9e10c15` |
| 9 | wsl.exe error text (UTF-16, on stdout) parsed as an HTTP response | `ed754a4` |
| 10 | materialized credentials unreadable by the island uid | `f5deecb` |
| 11 | bridge detection raced dockerd and lost on a clean machine | `444494d` |
| 12 | supervision installed below the already-running early return | `c13fd22` |
| 13 | islands could not reach host listeners on a native engine | `#378` |
| 14 | daemon updates reported failure at the moment they succeeded | `491c247` |
| 15 | first dial after an idle distro reported the host as down | `bb2947f` |

## Still open

**Socket exhaustion.** Under sustained dashboard use the WSL VM returns
`Wsl/Service/0x80072747` (WSAENOBUFS) and can wedge `wslservice.exe` itself,
needing `wsl --shutdown`. Seen three times in one evening on a freshly installed
distro, at shrinking intervals.

Two contributors are fixed and **neither is confirmed against the fault**:

- undrained response bodies — Go pools a connection only when the body is read
  to EOF; ours closed unread. Measured: 10 no-output requests opened 10
  connections (`internal/api/drain_test.go`).
- concurrent bursts wider than the pool — anything above `MaxIdleConnsPerHost`
  in one burst is dialled fresh and discarded. Capping concurrency is the right
  lever; raising the pool is not, because each idle connection HOLDS a
  subprocess.

**The instrument matters.** Sampling a `wsl.exe` process count aliases
100–300 ms processes and reads flat while thousands churn. Watch
`wslservice.exe` handle count and non-paged pool alongside PID *identity*.

**The architectural fix**, unbuilt: stop spawning a subprocess per dial. Have
the daemon listen on `127.0.0.1:<port>` inside WSL and let WSL2's localhost
forwarding reflect it onto Windows — plain TCP, standard pooling, zero
subprocesses. The open question is the security delta against today's 0600 unix
socket, since a localhost TCP port is reachable by any process on Windows.
AF_VSOCK was investigated and is blocked by a dynamic VmId with no unprivileged
API to resolve it.

## What actually found these

Not reasoning. Six confident diagnoses were wrong and the operator's own
terminal output corrected every one. Two of their observations did most of the
work:

- *"it never happened against the Mac mini"* — same client, same machine, same
  terminal, fine against a remote daemon and broken only against WSL. That
  identified the console bug, which no amount of reading the TUI would have.
- *"setup says it worked and nothing changed"* — that is the early return.

**The general lesson: on this path, ask for a log line or a number before
theorising.** Every hour lost was spent on a confident diagnosis that a single
command would have refuted.

## Testing lessons, which outlived the bugs

Six variants of one shape appeared in a week — a guard that reports green while
having no subject:

1. a mutation that never applied (the source held `\uXXXX` escapes; the patch
   carried the rendered glyph) — every mutation "passed"
2. a guard that matched the COMMENT explaining the code, not the code
3. a guard that SKIPPED on a missing marker; a skip prints `ok`
4. a test file named `*_windows_test.go`, silently excluded by the implicit GOOS
   constraint
5. an assertion pointed one clause away from the thing it named
6. a fixture that failed (macOS `sun_path` limit) and blamed the subject

Controls that came out of it, and now apply here as standard:

- **assert every mutation applies at exactly one site**, and that it compiles —
  `[build failed]` is not a guard firing
- **strip comments before matching source** (`internal/srcscan`)
- **a guard that cannot find what it checks must FAIL**, not skip and not pass
- **a fixture's failure must be distinguishable from its subject's** — let the
  setup error escape and fail by name
- **never pipe the gate into anything**; the pipeline reports the last command's
  status, not the checker's

See `docs/testing/guards-need-controls.md` for the full catalogue.
