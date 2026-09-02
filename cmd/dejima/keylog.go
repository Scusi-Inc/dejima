package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// DEJIMA_KEYLOG=<path> records every key the TUI receives, with a timestamp.
//
// IT EXISTS TO END AN ARGUMENT WITH DATA. An operator reported that the up arrow
// opened the audit ledger, Enter needed two presses and Esc did nothing. The
// diagnosis — a terminal delivering ESC [ A as three keypresses — explains all
// three and predicts which other keys misfire, and it has never been MEASURED.
// Four wrong diagnoses were produced from this island in a day on the same
// class of problem, each of them plausible, each corrected by the operator's own
// output.
//
// What the log answers that reasoning cannot:
//
//   - does an ESC arrive at all, or is it swallowed as a sequence start? The
//     escape-sequence guard in escseq.go arms on esc, so if esc never surfaces
//     the guard never runs and the fix is somewhere else entirely.
//   - are the bytes delivered separately, and how far apart? The timestamps
//     settle whether the 50ms window is the right size or nonsense.
//   - does Enter arrive twice, or arrive as something else once?
//
// Deliberately a file rather than the screen: the failure happens while someone
// is trying to use the dashboard, and a debug overlay would change the thing
// being measured. Off unless the variable is set, so it costs a nil check.
type keyLogger struct {
	mu sync.Mutex
	f  *os.File
	t0 time.Time
}

// openKeyLog returns a logger when DEJIMA_KEYLOG names a writable path, else
// nil. A path that cannot be opened is reported and then ignored: a diagnostic
// that refuses to start the program it is diagnosing is worse than no
// diagnostic.
func openKeyLog() *keyLogger {
	path := os.Getenv("DEJIMA_KEYLOG")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dejima: DEJIMA_KEYLOG=%s could not be opened (%v) — continuing without it\n", path, err)
		return nil
	}
	now := time.Now()
	fmt.Fprintf(f, "\n=== dejima key log — %s ===\n", now.Format(time.RFC3339))
	fmt.Fprintf(f, "# ms      type              key\n")
	return &keyLogger{f: f, t0: now}
}

// record writes one key. The elapsed-milliseconds column is what makes the log
// diagnostic rather than merely descriptive: three bytes of one arrow key arrive
// within a millisecond or two of each other, and a person's keystrokes do not.
func (k *keyLogger) record(msg tea.KeyMsg) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	fmt.Fprintf(k.f, "%-9.1f %-17s %q\n", float64(time.Since(k.t0).Microseconds())/1000, msg.Type.String(), msg.String())
}

// There is deliberately no Close. Each record is an unbuffered write straight to
// the fd, so nothing is pending when the process exits — and a close on a quit
// path is one more thing that can be missed on the paths that do not go through
// it (ctrl+c, a panic, the attach handoff that replaces the dashboard). A
// diagnostic that loses its last few lines exactly when the program went wrong
// would be missing the part worth reading.
