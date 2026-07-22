package service

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/fdlimit"
)

// The service templates must ask for a descriptor limit sized for island
// egress. launchd's default soft limit is 256, which a modest number of
// concurrent tunnels exhausts — and exhaustion presents as hung connections,
// not an error, so it is expensive to diagnose. Both templates are covered
// because the two install modes (LaunchAgent, LaunchDaemon) and the Linux unit
// are all shipped.

func TestPlistSetsFileLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{"LaunchAgent", map[string]any{
			"Label": "dev.dejima.dejimad", "ProgramArguments": []string{"/usr/local/bin/dejimad"},
			"WorkingDir": "/Users/x", "Home": "/Users/x",
			"StdoutPath": "/tmp/out.log", "StderrPath": "/tmp/err.log",
		}},
		{"LaunchDaemon", map[string]any{
			"Label": "dev.dejima.dejimad", "ProgramArguments": []string{"/usr/local/bin/dejimad"},
			"WorkingDir": "/Users/x", "Home": "/Users/x", "UserName": "x",
			"StdoutPath": "/tmp/out.log", "StderrPath": "/tmp/err.log",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderPlist(&buf, tc.data); err != nil {
				t.Fatalf("renderPlist: %v", err)
			}
			got := buf.String()
			if !strings.Contains(got, "<key>SoftResourceLimits</key>") {
				t.Error("plist has no SoftResourceLimits — daemon would inherit launchd's 256-file default")
			}
			if !strings.Contains(got, "<key>NumberOfFiles</key>") {
				t.Error("plist has no NumberOfFiles key")
			}
			want := fmt.Sprintf("<integer>%d</integer>", fdlimit.Target)
			if !strings.Contains(got, want) {
				t.Errorf("plist does not request %s\n---\n%s", want, got)
			}
		})
	}
}

func TestSystemdUnitSetsFileLimit(t *testing.T) {
	if !strings.Contains(systemdTemplate, "LimitNOFILE={{.LimitNOFILE}}") {
		t.Error("systemd unit does not set LimitNOFILE")
	}
}
