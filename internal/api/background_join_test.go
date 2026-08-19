package api

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/mailbox"
)

// Every test in this package that builds a Server routes it through
// joinBackground, so no detached work outlives the test that started it.
//
// The mailbox arrival hook is fired as `go fn(m)` and nothing used to wait for
// it. A hook that lands after its test has returned runs against whatever $HOME
// has become — the next test's t.TempDir, or the developer's real home once
// t.Setenv has restored it. That produced a flake ("unlinkat: directory not
// empty" during TempDir cleanup) which named a random victim test, because the
// goroutine belonged to an earlier one.
//
// paths.ProjectConfigPathRead removed the filesystem writes those hooks were
// doing. This removes the hooks themselves outliving the test, so the next hook
// that grows a side effect does not quietly re-open the same hole.

// joinBackgroundBudget is generous on purpose: it is a stuck-hook detector, not
// a performance assertion. Every hook in this package runs against fakes and
// finishes in microseconds.
const joinBackgroundBudget = 10 * time.Second

// joinBackground registers a cleanup that waits for s's detached work, and
// returns s so it can wrap a constructor inline.
func joinBackground(t *testing.T, s *Server) *Server {
	t.Helper()
	t.Cleanup(func() {
		if !s.WaitBackground(joinBackgroundBudget) {
			t.Errorf("a mailbox arrival hook was still running %s after this test finished. "+
				"It will now do its work against another test's HOME (or the real one). "+
				"Find what it is blocked on rather than raising this budget", joinBackgroundBudget)
		}
	})
	return s
}

// The seam has to actually join, not just exist. A hook that is still running
// when the test body ends must delay the cleanup rather than be abandoned —
// asserted by having the hook record its completion and checking the record
// after WaitBackground returns.
func TestWaitBackgroundJoinsAnInFlightHook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewServer(&fakeRuntime{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	landed := filepath.Join(t.TempDir(), "hook-finished")
	release := make(chan struct{})
	s.mailbox.SetArrivalHook(func(_ mailbox.Message) {
		<-release
		_ = os.WriteFile(landed, []byte("x"), 0o600)
	})

	s.mailbox.Send("isl", "a1", "", "topic", "body")
	if _, err := os.Stat(landed); err == nil {
		t.Fatal("the hook finished before it was released; this test is not measuring what it thinks")
	}

	close(release)
	if !s.WaitBackground(joinBackgroundBudget) {
		t.Fatal("WaitBackground gave up on a hook that was about to finish")
	}
	if _, err := os.Stat(landed); err != nil {
		t.Errorf("WaitBackground returned before the in-flight hook had finished: %v", err)
	}
}

// The budget must be a real deadline, or a genuinely stuck hook hangs the binary
// for ten minutes and blames whichever test the panic lands in — which is the
// misattribution this whole seam exists to prevent.
func TestWaitBackgroundGivesUpOnAStuckHook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewServer(&fakeRuntime{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) }) // let the goroutine exit with the test
	s.mailbox.SetArrivalHook(func(_ mailbox.Message) { <-block })

	s.mailbox.Send("isl", "a1", "", "topic", "body")
	if s.WaitBackground(50 * time.Millisecond) {
		t.Error("WaitBackground reported a clean drain while a hook was still blocked")
	}
}
