//go:build !windows

package fdlimit

import (
	"errors"
	"syscall"
)

// exhausted reports whether an accept error is descriptor exhaustion — this
// process is out (EMFILE) or the whole system is (ENFILE).
func exhausted(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

// softLimit returns the current RLIMIT_NOFILE soft limit, or 0 if unreadable.
// Reported alongside the warning so the operator sees the ceiling that was hit.
func softLimit() uint64 {
	var lim syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim) != nil {
		return 0
	}
	return uint64(lim.Cur)
}
