package main

import "testing"

// The Terminal section exists so "will my session render in full colour, and
// why" is answerable without attaching to an island and typing tmux commands at
// it. describeTerminal is the decision behind it, split from os.Getenv because
// the case that matters most — Windows, which sets no TERM — cannot be produced
// on the machines that run these tests.
func TestDescribeTerminal(t *testing.T) {
	cases := []struct {
		name                   string
		term, colorterm, wt    string
		wantTerm               string
		wantInferred, wantRich bool
	}{
		{"unix 256-colour terminal", "xterm-256color", "truecolor", "", "xterm-256color", false, true},
		{"screen/tmux flavours also pass the gate", "screen-256color", "", "", "screen-256color", false, true},
		// The regression this whole thread began with: a client reporting bare
		// `xterm` is deliberately excluded from RGB/sync/extkeys, so the report
		// must say "reduced" rather than implying something is broken.
		{"bare xterm is correctly reduced", "xterm", "", "", "xterm", false, false},
		// Windows Terminal: no TERM, but WT_SESSION identifies it and it can do
		// truecolour, so we infer — and the report must say it was inferred.
		{"windows terminal is inferred and rich", "", "", "abc-123", "xterm-256color", true, true},
		// Legacy conhost: nothing to infer from. Empty Term drives the WARN
		// branch in checkTerminal rather than a false OK.
		{"windows conhost has nothing to claim", "", "", "", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := describeTerminal(c.term, c.colorterm, c.wt)
			if got.Term != c.wantTerm {
				t.Errorf("Term = %q, want %q", got.Term, c.wantTerm)
			}
			if got.Inferred != c.wantInferred {
				t.Errorf("Inferred = %v, want %v", got.Inferred, c.wantInferred)
			}
			if got.Rich != c.wantRich {
				t.Errorf("Rich = %v, want %v", got.Rich, c.wantRich)
			}
		})
	}
}

// Docker and the island image are DAEMON-HOST facts. Probing them on the client
// told a Windows operator "the docker CLI isn't installed — islands run on it"
// about a machine that never runs islands, with the fix `make image` for a
// source tree it doesn't have — and made `dejima doctor` exit non-zero on a
// perfectly healthy client.
func TestDaemonElsewhere(t *testing.T) {
	t.Run("remote target: the daemon host is named", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("DEJIMA_HOST", "100.77.85.107:7273")
		where, remote := daemonElsewhere()
		if !remote {
			t.Fatal("a DEJIMA_HOST target must count as elsewhere")
		}
		if where == "" {
			t.Error("the report should name where the daemon actually is")
		}
	})

	t.Run("wsl target counts as elsewhere", func(t *testing.T) {
		// The daemon and Docker live inside the WSL2 distro, not on the Windows
		// side where the client runs — so probing locally is still wrong.
		t.Setenv("HOME", t.TempDir())
		t.Setenv("DEJIMA_HOST", "wsl://dejima")
		if _, remote := daemonElsewhere(); !remote {
			t.Error("a wsl:// target must count as elsewhere")
		}
	})

	t.Run("local socket: check here", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("DEJIMA_HOST", "")
		if where, remote := daemonElsewhere(); remote {
			t.Errorf("the local socket must be checked locally, got elsewhere=%q", where)
		}
	})
}
