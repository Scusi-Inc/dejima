package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/api"
)

// cliEnv and cliEnvFull build a REAL in-process daemon, so they inherit its
// detached work: the mailbox arrival hook, fired as `go fn(m)` with nothing
// waiting on it. A hook that lands after its test has returned does whatever it
// does against whatever $HOME has become — the next test's t.TempDir, or the
// developer's real home once t.Setenv has restored it.
//
// internal/api closed this for its own tests (mailbox.WaitArrivalHooks,
// Server.WaitBackground). This package has the same exposure through the same
// server and needs the same join.

// joinBackgroundBudget is a stuck-hook detector, not a performance assertion.
// Every hook here runs against a fake runtime and finishes in microseconds.
const joinBackgroundBudget = 10 * time.Second

// joinBackground registers a cleanup that waits for srv's detached work, and
// returns srv so it can wrap a constructor inline.
func joinBackground(t *testing.T, srv *api.Server) *api.Server {
	t.Helper()
	t.Cleanup(func() {
		if !srv.WaitBackground(joinBackgroundBudget) {
			t.Errorf("a mailbox arrival hook was still running %s after this test finished. "+
				"It will now do its work against another test's HOME (or the real one). "+
				"Find what it is blocked on rather than raising this budget", joinBackgroundBudget)
		}
	})
	return srv
}

// The wiring cannot be tested the ordinary way: these hooks finish in
// microseconds, so a helper that forgets the join passes every run until the day
// it doesn't, and then blames an unrelated test. Deleting the wrapper is a
// survivable mutation — which is another way of saying the wiring has no test.
//
// So this reads the package's own sources. A third cliEnv-alike is caught when
// it is written rather than when it flakes.
var apiNewServerCall = regexp.MustCompile(`\bapi\.NewServer\(`)

func TestEveryTestServerJoinsItsBackgroundWork(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var unwrapped []string
	seen := 0
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, "_test.go") || name == "background_join_test.go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			loc := apiNewServerCall.FindStringIndex(line)
			if loc == nil {
				continue
			}
			seen++
			if !strings.Contains(line[:loc[0]], "joinBackground(t, ") {
				unwrapped = append(unwrapped, name+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	// Without this the guard passes trivially the day the constructor is renamed
	// or the helpers move — reporting "all clear" on a package it is no longer
	// reading. Silence and success must not look the same.
	if seen == 0 {
		t.Fatal("found no api.NewServer calls at all — this guard is no longer watching anything")
	}
	if len(unwrapped) > 0 {
		t.Errorf("these test servers do not join their detached work — wrap them as "+
			"joinBackground(t, api.NewServer(...)):\n  %s", strings.Join(unwrapped, "\n  "))
	}
}

// The control on the control: the matcher must actually flag an unwrapped call.
// A future edit to the regex could otherwise leave the guard green and blind.
func TestJoinWiringGuardRecognisesAnUnwrappedCall(t *testing.T) {
	for _, c := range []struct {
		line string
		want bool
	}{
		{"\tsrv := api.NewServer(rt, log, nil)", true},
		{"\tsrv := joinBackground(t, api.NewServer(rt, log, nil))", false},
	} {
		loc := apiNewServerCall.FindStringIndex(c.line)
		flagged := loc != nil && !strings.Contains(c.line[:loc[0]], "joinBackground(t, ")
		if flagged != c.want {
			t.Errorf("guard flagged=%v want %v for %q", flagged, c.want, c.line)
		}
	}
}
