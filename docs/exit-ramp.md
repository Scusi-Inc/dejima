# Exit ramp — `dejima eject` (the no-lock-in guarantee)

Dejima is a *containment* runtime: your code and credentials live inside an
island, never spread across your host. The flip side of containment is the
promise that you can always get them **back out** — fully, on demand, with no
Dejima in the loop. `dejima eject` is that escape hatch.

This is the stronger sibling of `dejima uninstall --keep-islands`. That command
keeps your islands' named volumes on the host so a later reinstall *re-adopts*
them — no lock-in *within* Dejima. `eject` goes further: it copies the island's
contents onto the **plain host filesystem**, so the work keeps running even if
Dejima is gone entirely.

## Usage

```
dejima eject <island> <dest-dir> [--include-home] [--force] [--yes]
```

It writes:

| Path                   | Source volume                | Contents |
|------------------------|------------------------------|----------|
| `<dest-dir>/workspace` | `dejima-<island>-workspace`  | Your code **and its `.git`** — a usable git working tree. |
| `<dest-dir>/home`      | `dejima-<island>-home`       | Tool credentials + agent state. Only with `--include-home`. |

After an eject, `cd <dest-dir>/workspace` and you're in an ordinary git
checkout: build it, test it, `git commit`, `git push` — none of it touches
Dejima. That is the no-lock-in guarantee, made literal.

```
$ dejima eject my-island ~/escaped/my-island
ejecting "my-island" → ~/escaped/my-island (copying workspace volume, source read-only)…
  wrote ~/escaped/my-island/workspace (code + git history)

Ejected. ~/escaped/my-island/workspace is a plain git working tree — `cd` in and run it without Dejima.

$ cd ~/escaped/my-island/workspace && git log --oneline -1 && git status
```

## What's portable vs. island-specific

**Portable (the workspace).** The workspace volume is the source of truth for
your work: the full repository tree plus its `.git` directory (history,
branches, remotes). It is self-contained and host-independent — this is what
makes the extracted directory a real working tree, not a snapshot.

**Best-effort (the home volume, `--include-home`).** The home volume carries
tool credentials and agent/session state. Much of this is intentionally bound to
the island or the host on which it was created — device-scoped OAuth tokens,
session caches, paths that assume the island layout. It is copied verbatim, but
treat it as a convenience, not a guarantee: re-authenticating tools in their new
location is expected. Workspace portability is the load-bearing promise; home
portability is a bonus.

**Not copied.** Host-filesystem Port grants, MCP grants, and other
island-scoped Dejima brokering are *not* extracted — by design, they are deny-all
capabilities Dejima mediates and have no meaning outside it (this mirrors
`clone`, which also starts deny-all).

## Safety properties

- **The source volume is never mutated.** The copy runs in a throwaway container
  that mounts the volume **read-only** (`:ro`) and writes only to the host bind
  mount. Ejecting an island leaves it byte-for-byte unchanged and still usable.
- **No silent clobber.** A non-empty destination directory is refused; pass
  `--force` to overwrite an existing one deliberately.
- **Idle warning.** Like `clone`, eject is most consistent on an idle island.
  If the container is running, or there are uncommitted/unpushed changes, it
  warns (the copy is a point-in-time snapshot) but still proceeds — a rescue
  hatch must work even on a busy island.

## Where eject runs

`eject` writes to the host filesystem, so it must run on the island's Docker
host (it resolves the island through the daemon, then copies the volume with a
host-local `docker run`). On a remote daemon, run `eject` on that host.
