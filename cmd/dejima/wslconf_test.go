package main

import (
	"strings"
	"testing"
)

// wsl.conf belongs to the operator. Dejima adds one key to it and must not
// disturb anything else — a setup step that quietly resets someone's default
// user or hostname to install a daemon is a worse bug than the one it fixes.
func TestMergeWSLConf(t *testing.T) {
	const cmd = `setsid dejimad --foreground >>/root/.dejima/dejimad.log 2>&1`
	want := `command = "` + cmd + `"`

	for _, tc := range []struct {
		name     string
		existing string
		keep     []string // must survive untouched
		absent   []string
	}{
		{
			name:     "no file at all",
			existing: "",
		},
		{
			name:     "existing config with no [boot] section",
			existing: "[user]\ndefault = amanda\n\n[network]\nhostname = dejima\n",
			keep:     []string{"default = amanda", "hostname = dejima", "[user]", "[network]"},
		},
		{
			name:     "a [boot] section that has no command yet",
			existing: "[boot]\nsystemd = false\n",
			keep:     []string{"systemd = false"},
		},
		{
			// The operator's real file, verbatim. WSL's own systemd switch must
			// survive: turning it off would change how their distro boots.
			name:     "the operator's actual wsl.conf",
			existing: "[boot]\nsystemd=true\n",
			keep:     []string{"systemd=true"},
		},
		{
			name:     "an existing boot command is REPLACED, not duplicated",
			existing: "[boot]\ncommand = \"echo something-else\"\n",
			absent:   []string{"echo something-else"},
		},
		{
			name:     "[boot] in the middle keeps later sections",
			existing: "[user]\ndefault = amanda\n\n[boot]\ncommand = \"old\"\n\n[interop]\nenabled = true\n",
			keep:     []string{"default = amanda", "enabled = true", "[interop]"},
			absent:   []string{`"old"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeWSLConf(tc.existing, cmd)

			if !strings.Contains(got, want) {
				t.Errorf("boot command missing:\n%s", got)
			}
			if n := strings.Count(got, "[boot]"); n != 1 {
				t.Errorf("[boot] appears %d times, want exactly 1:\n%s", n, got)
			}
			// WSL takes the last command, so a duplicate would work by accident
			// and mislead anyone reading the file.
			if n := strings.Count(got, "command = "); n != 1 {
				t.Errorf("command appears %d times, want exactly 1:\n%s", n, got)
			}
			for _, k := range tc.keep {
				if !strings.Contains(got, k) {
					t.Errorf("destroyed the operator's own setting %q:\n%s", k, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("left stale content %q behind:\n%s", a, got)
				}
			}
		})
	}
}

// The merge must be idempotent: setup is re-run routinely, and a step that
// grows the file each time is how a config ends up with five boot commands.
func TestMergeWSLConfIsIdempotent(t *testing.T) {
	const cmd = `setsid dejimad --foreground >>/root/.dejima/dejimad.log 2>&1`
	once := mergeWSLConf("[user]\ndefault = amanda\n", cmd)
	twice := mergeWSLConf(once, cmd)
	if once != twice {
		t.Errorf("re-running setup changes the file again:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

// The key must sit directly under its section, with no gap. A blank line
// between "[boot]" and the command is still valid INI and still parses, but the
// file is one an operator opens and edits, and a key floating after a gap reads
// as belonging to nothing.
func TestMergeWSLConfLeavesNoGapInsideTheSection(t *testing.T) {
	const cmd = `setsid dejimad --foreground >>/root/.dejima/dejimad.log 2>&1`
	got := mergeWSLConf("[boot]\nsystemd=true\n", cmd)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			t.Errorf("blank line at %d inside a single-section file:\n%s", i, got)
		}
	}
}
