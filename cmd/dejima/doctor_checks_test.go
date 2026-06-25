package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
)

func TestFlagValue(t *testing.T) {
	args := []string{"/usr/local/bin/dejimad", "--tcp", ":7273", "--token-tcp", "127.0.0.1:7274", "--ssh", ":2222"}
	if got := flagValue(args, "--token-tcp"); got != "127.0.0.1:7274" {
		t.Errorf("token-tcp = %q", got)
	}
	if got := flagValue(args, "--tcp"); got != ":7273" {
		t.Errorf("tcp = %q", got)
	}
	if got := flagValue(args, "--nope"); got != "" {
		t.Errorf("missing flag = %q, want empty", got)
	}
	// flag at the end with no value → empty, no panic
	if got := flagValue([]string{"--token-tcp"}, "--token-tcp"); got != "" {
		t.Errorf("dangling flag = %q, want empty", got)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	loop := []string{"127.0.0.1:7274", "[::1]:7274", "localhost:7274", "127.0.0.1"}
	notLoop := []string{"0.0.0.0:7274", "100.77.85.107:7274", "minion:7273", "::"}
	for _, a := range loop {
		if !isLoopbackAddr(a) {
			t.Errorf("isLoopbackAddr(%q) = false, want true", a)
		}
	}
	for _, a := range notLoop {
		if isLoopbackAddr(a) {
			t.Errorf("isLoopbackAddr(%q) = true, want false", a)
		}
	}
}

func TestParseProgramArguments(t *testing.T) {
	plist := []byte(`<?xml version="1.0"?>
<plist version="1.0">
<dict>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/dejimad</string>
    <string>--tcp</string>
    <string>:7273</string>
    <string>--token-tcp</string>
    <string>127.0.0.1:7274</string>
  </array>
  <key>UserName</key>
  <string>aoos</string>
</dict>
</plist>`)
	args, err := parseProgramArguments(plist)
	if err != nil {
		t.Fatal(err)
	}
	// Must stop at </array> — not pick up <string>aoos</string> after it.
	want := []string{"/usr/local/bin/dejimad", "--tcp", ":7273", "--token-tcp", "127.0.0.1:7274"}
	if len(args) != len(want) {
		t.Fatalf("got %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestDiagnoseIslandSkew(t *testing.T) {
	const daemon = "v0.5.3"
	cases := []struct {
		name       string
		info       api.IslandInfo
		daemon     string
		wantStatus string
		wantInDet  string // substring expected in detail (empty = don't check)
		wantFix    string // exact fix expected (empty = don't check)
	}{
		{
			name:       "stale build, exact remedy",
			info:       api.IslandInfo{Name: "x", BuiltVersion: "v0.1.4"},
			daemon:     daemon,
			wantStatus: "WARN",
			wantInDet:  "built on v0.1.4, daemon on v0.5.3",
			wantFix:    "dejima upgrade x",
		},
		{
			name:       "upgrade stamp wins over build stamp",
			info:       api.IslandInfo{Name: "y", BuiltVersion: "v0.1.4", UpgradedVersion: "v0.5.3"},
			daemon:     daemon,
			wantStatus: "", // level after upgrade → nothing to report
		},
		{
			name:       "level island, no flag",
			info:       api.IslandInfo{Name: "z", BuiltVersion: "v0.5.3"},
			daemon:     daemon,
			wantStatus: "",
		},
		{
			name:       "newer island than daemon is not flagged",
			info:       api.IslandInfo{Name: "w", BuiltVersion: "v0.6.0"},
			daemon:     daemon,
			wantStatus: "",
		},
		{
			name:       "unknown provenance, dev daemon → no skew, no flag",
			info:       api.IslandInfo{Name: "u"},
			daemon:     "dev",
			wantStatus: "",
		},
		{
			name:       "zero-heartbeat alone warrants upgrade",
			info:       api.IslandInfo{Name: "h", BuiltVersion: "v0.5.3", NeverHeardFrom: true},
			daemon:     daemon,
			wantStatus: "WARN",
			wantInDet:  "no agent-state heartbeat",
			wantFix:    "dejima upgrade h (re-derives the managed hook shims)",
		},
		{
			name:       "stale build takes precedence over heartbeat (same remedy)",
			info:       api.IslandInfo{Name: "s", BuiltVersion: "v0.1.4", NeverHeardFrom: true},
			daemon:     daemon,
			wantStatus: "WARN",
			wantInDet:  "stale island image",
			wantFix:    "dejima upgrade s",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := diagnoseIslandSkew(tc.info, tc.daemon)
			if f.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (detail %q)", f.status, tc.wantStatus, f.detail)
			}
			if tc.wantInDet != "" && !strings.Contains(f.detail, tc.wantInDet) {
				t.Errorf("detail %q missing %q", f.detail, tc.wantInDet)
			}
			if tc.wantFix != "" && f.fix != tc.wantFix {
				t.Errorf("fix = %q, want %q", f.fix, tc.wantFix)
			}
		})
	}
}

func TestIslandSkewNote(t *testing.T) {
	if note := islandSkewNote(api.IslandInfo{Name: "x", BuiltVersion: "v0.1.4"}, "v0.5.3"); !strings.Contains(note, "dejima upgrade x") {
		t.Errorf("stale note = %q, want it to carry the upgrade remedy", note)
	}
	if note := islandSkewNote(api.IslandInfo{Name: "h", BuiltVersion: "v0.5.3", NeverHeardFrom: true}, "v0.5.3"); !strings.Contains(note, "no heartbeat") {
		t.Errorf("heartbeat note = %q", note)
	}
	if note := islandSkewNote(api.IslandInfo{Name: "z", BuiltVersion: "v0.5.3"}, "v0.5.3"); note != "" {
		t.Errorf("level island note = %q, want empty", note)
	}
	// Unknown daemon version (dev build) can't be ordered → no stale note.
	if note := islandSkewNote(api.IslandInfo{Name: "u", BuiltVersion: "v0.1.4"}, "dev"); note != "" {
		t.Errorf("dev-daemon note = %q, want empty", note)
	}
}

// fixProfilePort must normalize a port-less saved profile in place.
func TestFixProfilePort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := clientcfg.Save(clientcfg.Config{
		Profiles: []clientcfg.Profile{{Name: "minion", Host: "100.77.85.107"}},
	}); err != nil {
		t.Fatal(err)
	}
	msg, err := fixProfilePort("minion")
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Error("expected a non-empty outcome message")
	}
	cfg, _ := clientcfg.Load()
	if cfg.Profiles[0].Host != "100.77.85.107:7273" {
		t.Errorf("host = %q, want 100.77.85.107:7273", cfg.Profiles[0].Host)
	}
}
