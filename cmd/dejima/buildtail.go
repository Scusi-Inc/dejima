package main

import (
	"strings"
	"sync"
)

// buildTail keeps the LAST few lines written to it and discards the rest.
//
// The TUI used to pass io.Discard when building the island image, so a failed
// build surfaced as
//
//	✗ build island image: docker build failed: exit status 1
//
// and docker's own explanation — the line naming the package that would not
// install, the network that timed out, the disk that filled — went nowhere. An
// operator two thousand miles away was left with an exit code, which is not a
// bug report.
//
// The tail rather than the whole log because an island image build is thousands
// of lines and the useful part is always at the end; and bounded because this
// runs inside a TUI that must not grow a buffer for the length of a build.
type buildTail struct {
	mu    sync.Mutex
	lines []string
	max   int
	part  strings.Builder // a partial final line between writes
}

func newBuildTail(max int) *buildTail { return &buildTail{max: max} }

func (b *buildTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.part.Write(p)
	s := b.part.String()
	if !strings.Contains(s, "\n") {
		return len(p), nil
	}
	parts := strings.Split(s, "\n")
	// The last element is whatever follows the final newline: still incomplete.
	b.part.Reset()
	b.part.WriteString(parts[len(parts)-1])
	for _, l := range parts[:len(parts)-1] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		b.lines = append(b.lines, l)
	}
	if n := len(b.lines); n > b.max {
		b.lines = append([]string(nil), b.lines[n-b.max:]...)
	}
	return len(p), nil
}

// String returns the retained tail, including any unterminated final line —
// which is where a build that dies mid-write leaves its most recent output.
func (b *buildTail) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]string(nil), b.lines...)
	if rest := strings.TrimSpace(b.part.String()); rest != "" {
		out = append(out, rest)
	}
	return strings.Join(out, "\n")
}
