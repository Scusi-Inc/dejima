package main

import (
	"testing"

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
