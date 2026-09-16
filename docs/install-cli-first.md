# One line, then the TUI: retiring the install-time host/client/local fork

**Status:** designed, not built. Supersedes the three-way installer question that
[#439](https://github.com/Scusi-Inc/dejima/pull/439) had just finished making
*correct* — the fork works now; this argues it should not exist.

**Prompted by** an operator who described herdr's model and asked for it: one
line on the client, then manage any host from the TUI.

---

## The ask, in one line

```
curl -fsSL https://dejima.tech/install.sh | sh     # a CLI. seconds. asks nothing.
dejima                                            # the TUI routes from here
```

No `install.sh` vs `install-client.sh`. No question about what this machine is
before anything is installed. The machine's role is decided *later, in the TUI,
in context* — and a machine that should run islands can be provisioned **from**
the client, over SSH, without logging into it.

## Why the current shape is wrong

Not that it is broken. It is that **the same question is asked in three places**,
and the front door asks it at the moment the operator knows least.

| # | asks | reads | when |
|---|------|-------|------|
| 1 | `install.sh` | `DEJIMA_SETUP` / `DEJIMA_ROLE` | before Go, Docker, or a clone |
| 2 | `scripts/setup.sh` → `lib/dest.sh` | `DEJIMA_SETUP` | ~10 min later, mid-install |
| 3 | `dejima onboard` first-run router | — | first run of the CLI |

#439 made 1 and 2 agree and stop double-asking. It did nothing about 3, because 3
is *the good one*: it asks after the CLI exists, it can show the machine's actual
state, and its answer can be changed later without re-running an installer.

Three failures observed this week trace to the fork being at install time:

- **A stale `DEJIMA_HOST`** from a prior client install left the CLI talking to
  an old server while a fresh local daemon ran unseen. Detected and named in
  #439; it cannot happen if installing does not pick a role.
- **Two menus ten minutes apart**, with different words and a third option that
  was not on the first — the operator answers before they can see the machine.
- **The heaviest possible first step.** `install.sh` installs Go, clones the
  repo, and builds. An operator who only wanted to drive a server elsewhere paid
  all of it. That is what `install-client.sh` exists to avoid, and nothing at the
  front door points at it until the question is answered.

## What already exists (most of it)

This is the part that makes the change small, and it was the surprise of the
design pass. **Almost nothing new has to be written except the remote path.**

| capability | where | state |
|---|---|---|
| one-line CLI install, prebuilt binary, no toolchain | `install-client.sh` (GitHub release asset) | **works** |
| three-way router, local first, on first run | `cmd/dejima/onboard.go:155+` | **works** |
| Windows → WSL2 local | `firstRunSetUpWSL` | **works** |
| join a server by invite blob | `firstRunJoin` / `dejima join` | **works** |
| several hosts, switchable | `dejima profile ls/add/rm/switch` | **works** |
| switch host inside the TUI | `cmd/dejima/tui_switch.go` | **works** |
| local provisioning (Docker, image, daemon) | `scripts/setup.sh`, `dejima onboard` | **works** |
| **provision a REMOTE host from the client** | — | **missing** |

So the target model is mostly a matter of *deleting a door and renaming another*,
plus one genuinely new capability.

## The target

### Phase 1 — the front door installs a CLI and asks nothing

`https://dejima.tech/install.sh` serves what is today `install-client.sh`: fetch
the release asset for this platform, verify it, drop `dejima` in `$PREFIX/bin`,
exit. No Go, no Docker, no clone, no question.

- The current source-building `install.sh` becomes `install-from-source.sh`, kept
  and documented. It is the right tool for a contributor, a machine with no
  release asset, or a pinned `DEJIMA_REF` — it is just not the front door.
- **The `install.sh` URL must keep working, and that constrains the rename.**
  Three places on the site reference it, plus every runbook, blog post and
  terminal-history line in the wild. The URL keeps its name; the *contents*
  change. A machine that pipes it expecting a server build gets a CLI instead —
  which is the point, and is a behaviour change to call out in release notes.
- `DEJIMA_SETUP` / `DEJIMA_ROLE` stop being read by the front door. They keep
  working on `install-from-source.sh` and on `scripts/setup.sh`, where scripted
  callers already set them.

**Non-interactive callers are the risk here**, and they are the reason this is
its own phase. Every CI job and runbook that pipes `install.sh` today gets a
host install; after this they get a CLI and no daemon. That is a real break, it
is silent, and it cannot be detected from inside the script. Mitigation: ship it
in a release whose notes lead with it, and have the CLI print, on first run with
no daemon, exactly what to run to get one.

### Phase 2 — the TUI owns the no-daemon state

Today a first run with no daemon goes through `onboard`'s router, which is good,
but it is a CLI prompt that runs *before* the dashboard. After Phase 1 it becomes
the common path rather than an edge case, so it moves into the TUI proper:

```
┌─ Dejima ──────────────────────────────────────────┐
│  No daemon configured.                            │
│                                                   │
│    l  Use this machine     Docker + daemon here   │
│    r  Add a host over SSH  provision another box  │
│    c  Paste an invite      someone already has one│
└───────────────────────────────────────────────────┘
```

Same three destinations as `onboard`, same order, same default — this is a
surface change, not a new decision tree. The router logic stays in `onboard.go`
and the TUI calls it, so there is one implementation of the question and the
`dejima onboard` command keeps working for people who type it.

`l` runs the existing local provisioning. `c` runs the existing invite path.
`r` is new.

### Phase 3 — `dejima host add <ssh-target>`

The capability that earns the claim. This is the herdr trick, adapted to the fact
that Dejima's payload is heavier than a single Rust binary.

```
dejima host add minion
dejima host add ssh://aoos@10.0.0.4:22
```

Reading the operator's `~/.ssh/config` for host aliases, as herdr does — the
alias is already the name they know the machine by.

**The sequence**, each step reporting and each step re-runnable:

1. **Reach it.** `ssh -o BatchMode=yes <target> true`. Fail here with the ssh
   error verbatim, not a paraphrase — ssh's own diagnostics are better than ours.
2. **Identify it.** `uname -s -m` → pick the release asset. Refuse a platform we
   publish no asset for, rather than falling back to a source build over SSH.
3. **Check for a daemon.** A compatible `dejimad` already on `PATH` is used as-is.
   A version mismatch prompts to upgrade; **non-interactive refuses rather than
   modifying the host.** (Herdr's rule, and it is the right one.)
4. **Check for Docker.** This is the step with no single answer and the reason
   Phase 3 is hard — see below.
5. **Install `dejima` + `dejimad`** from the release asset, checksum-verified
   against `SHA256SUMS`, into `/usr/local/bin` (sudo prompted through the tty).
6. **`dejimad service install`** on the remote, with a listener the client can
   reach. Tailnet if Tailscale is present on both ends; otherwise loopback +
   an SSH tunnel, which needs no inbound firewall change.
7. **Mint an invite on the remote and consume it locally** — reusing
   `dejima join`'s existing blob rather than inventing a second enrollment path.
   The new host appears as a profile; the TUI can switch to it.
8. **Build the island image** on the remote, in the background, reporting
   progress. Minutes, and the operator should not be blocked at a prompt for it.

**Step 4 is the honest hard part.** Herdr pushes one static binary; Dejima needs
a container engine. Installing Docker unattended over SSH is distro-specific,
needs root, may need a group change that requires a re-login, and on macOS means
Docker Desktop or colima with a GUI first-run. The design does **not** pretend to
solve this in general:

- **Docker present and reachable** → proceed.
- **Absent, Linux, known distro** → offer the exact one-liner, run it on
  confirmation, verify, continue.
- **Absent, anything else** → stop with the precise next step and how to resume.
  A partially provisioned host is worse than one that clearly stopped, and
  `dejima host add` must be safe to re-run after the operator fixes it by hand.

`dejima onboard --provision-host` already encodes much of this knowledge for the
local case (`cmd/dejima/provision.go`, 6-phase detect→act→verify with resume).
Phase 3 should reuse that state machine over an SSH transport rather than growing
a second one — the phases and their verifications are the same; only the command
runner changes.

## Security

Pushing binaries to a machine over SSH is a real trust move and deserves stating.

- **Checksum-verify every asset against published `SHA256SUMS`**, on the remote,
  before it is made executable. The same rule the self-update path already holds.
- **Never silently modify a host.** Interactive runs prompt before install or
  upgrade; non-interactive runs fail. This is the difference between a
  provisioning tool and a worm, and it is one flag away from being wrong.
- **The invite carries a token.** It is minted on the remote and consumed
  locally over the same SSH channel; it must not be echoed to a log or left in
  shell history.
- **Prefer the tunnel to a new listener.** An SSH tunnel to a loopback-bound
  daemon opens no inbound port and needs no firewall change. A tailnet listener
  is better UX where Tailscale already exists; a WAN-reachable listener is never
  offered by default.

## What this does not change

- **The physical asymmetry is real and stays.** A machine that runs islands needs
  Docker and a multi-GB image; a machine that drives one needs 15MB. Nothing here
  makes that go away. What goes away is **being asked about it before anything is
  installed** — the capability is provisioned when and where it is wanted.
- **`scripts/setup.sh` and its `dest.sh` question stay**, for the from-source path
  and for scripted callers that set `DEJIMA_SETUP`. It stops being something a
  first-time operator meets.
- **No change to the island model**, containment, or the daemon's trust boundary.
  This is entirely about how the daemon gets onto a machine.

## Relationship to herdr

[herdr](https://herdr.dev/) is where this model comes from and it is worth being
precise about what is being borrowed. Two things: **install a client, decide
later**, and **provision the remote from the client over SSH, with a
no-silent-modification rule.** Both are transport and UX patterns, adoptable
without any dependency on herdr.

What is deliberately *not* borrowed is their architecture. herdr's
[comparison page](https://herdr.dev/compare/) does not mention containerization,
isolation or sandboxing — by design; they run wherever a terminal and SSH reach.
That is the opposite of Dejima's thesis, and it is why this doc copies their
front door and nothing else.

## Open questions

1. **Does the `install.sh` URL change contents, or do we add a new URL?** This doc
   assumes contents change and the name is preserved, because the name is what is
   in the wild. The cost is a silent behaviour change for non-interactive callers.
2. **Windows.** `dejima host add` to a Windows target has no sensible meaning —
   the daemon needs Linux. Refuse with the WSL2 path, or omit the target type?
3. **Does `dejima host add` imply `dejima host rm`,** and does removing a host
   mean forgetting a profile or tearing down a daemon? These must not be the same
   verb.
4. **Reusing `provision.go` over SSH** assumes its steps are transport-agnostic.
   That is a code reading, not a verified property — confirm before committing to
   it, since a second state machine is the alternative.
