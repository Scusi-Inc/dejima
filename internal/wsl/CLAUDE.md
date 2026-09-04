# internal/wsl — the channel eats things

Everything here crosses `wsl.exe`, and that boundary drops or mangles more than
it looks. Four rules, each of which was learned from a real operator's machine
after a surface reported healthy.

**1. No shell variables in anything sent through `wsl.exe`. Resolve in Go,
interpolate the literal.** `startDaemonInWSL` has said this for weeks. The DIAL
did not get the same treatment, and on 2026-09-03 the newline between
`export HOME` and `S=...` most likely joined into `export HOMES=` — leaving `S`
unset and asking socat to connect to `""`. Prior casualties in this same file:
`sh: 18: [: Illegal number` from a variable that "arrived empty with its quotes
intact", and `mkdir: cannot create directory (empty)` from an unset HOME.
Multi-line scripts are the highest-risk form; prefer one line.

**2. `wsl.exe -d <distro> -- sh -c ...` does NOT pass HOME, and dash does not
synthesise it.** Anything deriving a path from `$HOME` gets `/.dejima/...`.
Resolve HOME through `Run` and bake the result in.

**3. `[ -S sock ]` is not liveness.** It answers "is there a socket file". A
daemon that dies without unlinking — which is every time WSL terminates an idle
distro — leaves the file behind and the check passes forever, so the distro
reports READY while every connect is refused. Connect, do not stat.

**4. When a surface truncates an error, get the untruncated one before
theorising.** The TUI clipped `socat E connect(, AF=1 "<anon>", 2): Invalid
argument` at `contex…`, and three hypotheses were built on the visible half.
`AF=1`, the sockaddr length and the errno were all needed and all past the cut.

Background: [docs/wsl-windows-postmortem.md](../../docs/wsl-windows-postmortem.md).
