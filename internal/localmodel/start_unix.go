//go:build !windows

package localmodel

import "syscall"

// detachAttrs puts the spawned server in its OWN SESSION.
//
// setsid, not nohup. `nohup` only ignores SIGHUP; it does not leave the session,
// so the process still dies with the one that started it. The daemon's WSL
// launcher learned this on a real machine and wrote it down — "`nohup … &` and
// `setsid nohup … </dev/null &` were both tried on a real machine and both left
// no socket and no process once the Windows window closed" — and the macOS
// install fallback was then written with nohup anyway. It reported "starting the
// server directly" and left `installed (not running)` on the operator's screen.
func detachAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
