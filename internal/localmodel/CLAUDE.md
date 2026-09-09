# internal/localmodel — the daemon has no session and no PATH

This package runs inside the daemon, which is a launchd/systemd service. It has
no terminal, no login shell, no populated PATH and no GUI session. Every rule
here is one of those absences, and each was learned after an install reported
success and left nothing running.

**1. Never resolve an external binary by bare name.** Absolute candidates first,
`exec.LookPath` only as a fallback. This has now been fixed three times in this
area — `resolveExe`, `findBrew`, `vmmem` — and the third was written by someone
who had read the first two. A launchd daemon's PATH is `/usr/bin:/bin:/usr/sbin:/sbin`,
which is why `sysctl` "worked" in a shell and returned host RAM as 0 in service.

**2. `nohup` does not survive here; use `setsid`.** `nohup` only ignores SIGHUP,
it does not leave the session. `internal/wsl` recorded this from a real machine
— both `nohup` and `setsid`+`nohup` were tried and both left no process — and
the macOS fallback was written with `nohup` anyway, the same week, by someone who
had read that comment. Knowledge does not cross a package boundary by being
written well.

**3. An install that starts nothing is not a finished install.** `brew services`
fails where the daemon has no launchd user domain to bootstrap into
(`launchctl enable gui/501/... exited with 125`). Start must WAIT FOR THE BACKEND
TO ANSWER before returning, or "installed" and "usable" diverge with no surface
reporting the gap.

**4. A refusal must be true of the thing it names.** `local install` refused on
macOS because "its installer needs sudo" — true of the official installer,
false of Homebrew, which refuses to run as root and installs into a user-owned
prefix. The message sent operators to run by hand the command the daemon could
have run itself.

**5. Never shell out to the backend CLI without `EnsureRunning`.** Every command
here is a CLIENT of a server, so a stopped backend fails them all — and rule 3's
gap is routine, not rare (any host reboot). `install` called `Start`; `pull` and
`rm`, which operators run far more often, went straight to the CLI and relayed
its `run 'ollama serve'` to a WINDOWS operator whose backend was on a Mac mini.
That is rule 4 arriving by a different door: **a message we pass through is a
message we send**, and it must be true of the machine that reads it. Guarded by
`TestLocalPull*` in `internal/api` (via the `Server.localBE` seam) and
`TestEnsureRunning*` here.

Background: [docs/local-models.md](../../docs/local-models.md).
