package fdlimit

import (
	"errors"

	"golang.org/x/sys/windows"
)

// exhausted reports whether an accept error is socket/handle exhaustion. The
// Windows sockets equivalent of EMFILE is WSAEMFILE; WSAENOBUFS covers the
// system running out of buffer space for a new socket.
func exhausted(err error) bool {
	return errors.Is(err, windows.WSAEMFILE) || errors.Is(err, windows.WSAENOBUFS)
}

// softLimit has no analogue on Windows — there is no settable per-process
// descriptor ceiling to report.
func softLimit() uint64 { return 0 }
