//go:build !windows

package fdlimit

import (
	"fmt"
	"syscall"
)

// Raise lifts RLIMIT_NOFILE's soft limit toward Target, never above the hard
// limit. It is best-effort by design: a daemon that cannot raise its limit
// should still start and serve, just with less headroom, so callers log the
// error rather than treating it as fatal.
//
// The retry loop is for macOS. Darwin's hard limit is frequently reported as
// RLIM_INFINITY, but the kernel silently caps a process at
// kern.maxfilesperproc and fails the setrlimit outright rather than clamping.
// Rather than read the sysctl, halve the request until one sticks — this lands
// on the real ceiling in a handful of iterations on any system.
func Raise() (Result, error) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return Result{}, fmt.Errorf("get RLIMIT_NOFILE: %w", err)
	}
	res := Result{Was: uint64(lim.Cur), Now: uint64(lim.Cur), Max: uint64(lim.Max)}

	want := Target
	if res.Max != ^uint64(0) && want > res.Max {
		want = res.Max
	}
	if want <= res.Was {
		return res, nil // already have at least as much headroom as we'd ask for
	}

	var lastErr error
	for want > res.Was {
		next := lim
		next.Cur = want
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &next); err == nil {
			res.Now, res.Raised = want, true
			return res, nil
		} else {
			lastErr = err
		}
		want /= 2
	}
	return res, fmt.Errorf("set RLIMIT_NOFILE to %d: %w", Target, lastErr)
}
