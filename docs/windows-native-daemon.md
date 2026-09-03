# Running Dejima natively on Windows — research

Status: **research only, nothing committed to.** Captured 2026-08-12 so the
findings aren't re-derived later. See `docs/roadmap.md` for where this sits.

## Bottom line

The daemon is much closer to Windows than "Unix-only" suggests. It already
**cross-compiles and vets clean** for `windows/amd64`:

```
GOOS=windows GOARCH=amd64 go build ./cmd/dejimad   # exit 0
GOOS=windows go vet ./...                          # clean, whole tree
```

There is exactly **one functional blocker**, and it is one function with two
callers. Everything else either already works or degrades without crashing.

But read the strategic caveat at the bottom before pricing this: a native
Windows daemon is a **packaging and UX win, not an isolation win**. It does not
give you anything WSL2 doesn't.

## The one blocker: `startPTY`

`internal/bridge/session.go` needs a host PTY to run `docker exec -it`. It uses
`github.com/creack/pty`, whose Windows build is a stub:

```go
// creack/pty v1.1.24 — start_windows.go
func StartWithSize(cmd *exec.Cmd, ws *Winsize) (*os.File, error) {
	return nil, ErrUnsupported
}
```

No `pty_windows.go`, no ConPTY, no partial support. It compiles and fails at
runtime, which is why the build being green proves less than it appears to.

Blast radius is two call sites:

| Caller | Feeds | Matters? |
|---|---|---|
| `ExecPTY` (`session.go:149`) | every agent session + the SSH façade | **yes — this is the product** |
| `HostPTY` (`session.go:186`) | host terminals (a shell *on the daemon host*) | optional; gated behind `--host-terminals` |

So a first cut can ship with host terminals unsupported on Windows and lose
nothing that matters.

## What already works

- **The container runtime layer.** `internal/runtime/docker.go` shells out to
  the `docker` CLI (`exec.CommandContext(ctx, d.bin(), args...)`) rather than
  using the Engine API. `docker.exe` is native on Windows, so ~23 shell-out
  sites port with no change.
- **Supervision degrades, doesn't crash.** `service.DetectMode` returns
  `{Mode: "unknown", Summary: "supervision detection unsupported on windows"}`
  (`internal/service/detect.go:33`). No auto-start, but a hand-run daemon works.
- **Platform fallbacks already exist** for the permission-sensitive bits —
  `mcpbroker/owner_other.go`, `capability/owner_other.go`,
  `fdlimit/guard_unix.go` + friends, `cmd/dejima/doctor_owner_other.go`. Somebody
  already thought about non-Unix builds.
- **The island image is unaffected.** tmux and the agent all run *inside* a Linux
  container; nothing there cares what the host is.

## Paths

### A. ConPTY behind the existing interface — smallest change

Keep shelling out to `docker exec -it`; give it a ConPTY on Windows. Add
`internal/bridge/pty_windows.go` implementing `startPTY` via
`CreatePseudoConsole`, and leave the Unix build on creack/pty.

Candidate libraries to evaluate (none vetted yet): `github.com/aymanbagabas/go-pty`
(cross-platform, ConPTY backend), `github.com/UserExistsError/conpty`,
`github.com/photostorm/pty` (a creack fork carrying Windows support).

- **Pro:** touches one file; the rest of the daemon is already Windows-clean.
- **Con:** inherits ConPTY's quirks — the same class of bug that produced the
  left-column smearing (`10dce1a`, `7396966`). We would be putting ConPTY on
  *both* ends of the connection, which is exactly where that pain came from.

### B. Drop the host PTY entirely — the better fix

Replace the `docker exec -it` shell-out on the session path with the **Docker
Engine API**: `ContainerExecCreate{Tty: true}` + `ContainerExecAttach` returns a
hijacked bidirectional stream. **No host PTY on any platform** — the TTY lives
in the daemon-side container, where it always belonged. The SDK speaks
`npipe:////./pipe/docker_engine` on Windows and the unix socket elsewhere.

- **Pro:** removes `creack/pty` from the session path outright; identical code on
  every OS; kills the ConPTY-on-the-host class of bug rather than porting it.
  Probably also cheaper than fork/exec-ing the CLI per attach.
- **Con:** a real refactor of `internal/bridge` (`AttachToTmux`, `ExecPTY`,
  `PTYSession`, `Resize`) plus a new dependency on the Docker SDK. `HostPTY`
  still needs a genuine pty and would stay Unix-only (acceptable — see the table
  above). Needs care to keep the initial-size handshake that `702cec3` added.

**B is the recommended direction** if this is done at all: it is the only option
that makes Windows work *and* leaves the Unix path better than it found it.

### C. WSL2 — already built

`agent/d4` (`35f929c`) runs dejimad inside WSL2 and reaches it from the Windows
client over `wsl://<distro>`. Ships now, no runtime work, no ConPTY exposure.

## Secondary work, if A or B is pursued

- **Supervision:** a Windows Service or Task Scheduler backend in
  `internal/service` (currently launchd + systemd only).
- **Provisioning:** `dejima onboard --provision-host` is Homebrew/colima-centric
  (`cmd/dejima/provision.go`); Windows would want Docker Desktop detection.
- **Local socket trust — a design question, not plumbing.** `clientForHost("")`
  treats the Unix socket as *filesystem-trusted, acting as OWNER*, and
  deliberately ignores `DEJIMA_TOKEN` there. Go can listen on AF_UNIX on
  Windows 10 1803+, but the **permission model behind that trust does not
  carry**. This needs an explicit decision (named pipe with an ACL? require a
  token locally?) before a Windows daemon is safe to expose, and it should not
  be discovered late.

## Strategic caveat — read before pricing this

Docker Desktop on Windows runs Linux containers inside its own VM (WSL2 or
Hyper-V backend). So a *native* Windows dejimad still drives a Linux VM to host
islands. Compared with path C, native buys:

- **no WSL distro for the user to install, name, or keep running**, and one less
  moving part in onboarding;
- a daemon that can be supervised as a first-class Windows service.

It does **not** buy stronger isolation, different container semantics, or the
ability to run islands without a Linux VM. The containment story is identical.

So the honest framing: **C (WSL2) already delivers the capability**; A/B are about
making it feel native. That ordering should drive priority — ship C, learn from
real Windows users, and only then decide whether B is worth the refactor.
