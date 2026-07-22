// Package fdlimit raises the daemon's open-file limit at startup.
//
// Why this exists: dejimad is fd-hungry in a way that isn't obvious from its
// small memory footprint. Every island egress CONNECT tunnel costs TWO
// descriptors (the hijacked client conn and the dialed target conn) and lives
// as long as the agent's HTTPS session does — and a single Claude Code agent
// holds many concurrent connections to the LLM API. Add the API/TCP/SSH
// listeners and the pipes of every `docker exec` child and the working set is
// easily hundreds.
//
// Meanwhile launchd hands a LaunchAgent whatever `launchctl limit maxfiles`
// says, which on stock macOS is a soft limit of 256. A daemon started from a
// shell inherits the shell's much larger limit, so this only bites the
// service-managed install — which is exactly the one operators run
// unattended. Exhaustion does not look like exhaustion: accept(2) starts
// failing with EMFILE, net/http treats that as retryable and keeps looping, so
// clients complete the TCP handshake into a backlog nobody drains and hang
// until they time out. Once the backlog fills the kernel refuses outright.
// "Some connections hang, some are refused, a restart fixes it for an hour"
// is the signature.
//
// Raising the limit in-process fixes already-installed daemons without waiting
// for a reinstall; the service templates also set it so a fresh install starts
// correct. Both are cheap, and they are independent.
package fdlimit

import (
	"fmt"
	"syscall"
)

// Target is the soft limit we want. It is far above any plausible working set
// (a busy host runs in the low hundreds) and far below the per-process ceiling
// on both macOS and Linux, so Raise normally reaches it exactly.
const Target uint64 = 16384

// Result reports what Raise did, for logging. Raised is false when the limit
// already met Target and nothing was changed.
type Result struct {
	Was    uint64 // soft limit before
	Now    uint64 // soft limit after
	Max    uint64 // hard limit (RLIM_INFINITY reported as ^uint64(0))
	Raised bool
}

// String renders the result for a log line.
func (r Result) String() string {
	max := "unlimited"
	if r.Max != ^uint64(0) {
		max = fmt.Sprintf("%d", r.Max)
	}
	return fmt.Sprintf("soft=%d (was %d, hard=%s)", r.Now, r.Was, max)
}

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
