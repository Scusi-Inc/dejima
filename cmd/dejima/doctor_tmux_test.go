package main

import (
	"errors"
	"strings"
	"testing"
)

// The point of this check is that the raw failure names no remedy, so the
// remedy is what gets asserted — a FAIL that says "tmux is missing" and stops
// there would reproduce the original complaint inside doctor.
func TestTmuxStatus(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		level, detail, fix := tmuxStatus("/opt/homebrew/bin/tmux", nil, "darwin")
		if level != "OK" {
			t.Errorf("level = %q, want OK", level)
		}
		if !strings.Contains(detail, "/opt/homebrew/bin/tmux") {
			t.Errorf("detail should name the resolved path, got %q", detail)
		}
		if fix != "" {
			t.Errorf("a working tmux needs no fix hint, got %q", fix)
		}
	})

	t.Run("missing names an install command", func(t *testing.T) {
		for _, tc := range []struct{ goos, want string }{
			{"darwin", "brew install tmux"},
			{"linux", "apt install -y tmux"},
			{"windows", "wsl"},
		} {
			level, detail, fix := tmuxStatus("", errors.New("not found"), tc.goos)
			if level != "FAIL" {
				t.Errorf("%s: level = %q, want FAIL", tc.goos, level)
			}
			if !strings.Contains(fix, tc.want) {
				t.Errorf("%s: fix = %q, want it to contain %q", tc.goos, fix, tc.want)
			}
			// The operator's actual question was "whose problem is this".
			if !strings.Contains(detail, "pty start") {
				t.Errorf("%s: detail should quote the error the operator saw, got %q", tc.goos, detail)
			}
		}
	})
}

// The island bundling tmux is what makes the host requirement surprising, so
// the fix hint has to say so explicitly rather than leaving the operator to
// wonder why a bundled dependency needs installing.
func TestTmuxFixExplainsWhyTheHostNeedsIt(t *testing.T) {
	_, _, fix := tmuxStatus("", errors.New("not found"), "darwin")
	if !strings.Contains(fix, "island image bundles tmux") {
		t.Errorf("fix should explain the island-vs-client distinction, got %q", fix)
	}
}
