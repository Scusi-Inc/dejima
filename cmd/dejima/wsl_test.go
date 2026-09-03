package main

import (
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/wsl"
)

// A wsl:// target must route to the WSL client, never to the TCP one — a TCP
// dial of "wsl://dejima" would fail deep in net/http with an opaque address
// error. Off Windows the routing still happens; it's the transport that
// refuses, and it must refuse with the reason.
func TestClientForHostRoutesWSL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := clientForHost("wsl://dejima")
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("a wsl:// target should build a client on Windows: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("off Windows a wsl:// target must fail, not silently build a TCP client")
	}
	if !strings.Contains(err.Error(), "Windows") {
		t.Errorf("error should explain the platform limit, got: %v", err)
	}
}

// The profile name is what shows up in `dejima profile ls`, so the default
// distro gets the clean "wsl" label and a custom one is disambiguated.
func TestProfileNameFor(t *testing.T) {
	cases := map[string]string{
		"":                "wsl",
		wsl.DefaultDistro: "wsl",
		"Ubuntu-22.04":    "wsl-Ubuntu-22.04",
	}
	for in, want := range cases {
		if got := profileNameFor(in); got != want {
			t.Errorf("profileNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// setup must be re-runnable: a second run updates the existing profile in place
// rather than erroring on the duplicate name or stacking a second entry.
func TestSaveWSLProfileIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := saveWSLProfile("dejima"); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := saveWSLProfile("dejima"); err != nil {
		t.Fatalf("second save should update in place, got: %v", err)
	}

	cfg, err := clientcfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 1 {
		t.Fatalf("expected exactly one profile, got %d: %+v", len(cfg.Profiles), cfg.Profiles)
	}
	if cfg.Profiles[0].Name != "wsl" || cfg.Profiles[0].Host != "wsl://dejima" {
		t.Errorf("profile = %+v, want {wsl wsl://dejima}", cfg.Profiles[0])
	}
	if cfg.ActiveProfile != "wsl" {
		t.Errorf("setup should make the WSL host active, got %q", cfg.ActiveProfile)
	}
}

// A second distro gets its own profile alongside the first, so someone can keep
// a scratch distro and their main one.
func TestSaveWSLProfileSeparateDistros(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := saveWSLProfile("dejima"); err != nil {
		t.Fatal(err)
	}
	if err := saveWSLProfile("scratch"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := clientcfg.Load()
	if len(cfg.Profiles) != 2 {
		t.Fatalf("expected two profiles, got %+v", cfg.Profiles)
	}
	if cfg.ActiveProfile != "wsl-scratch" {
		t.Errorf("the most recent setup should be active, got %q", cfg.ActiveProfile)
	}
	// And the saved host must survive resolveTarget → clientForHost unmangled.
	h, err := cfg.LookupProfile("wsl-scratch")
	if err != nil {
		t.Fatal(err)
	}
	if h != "wsl://scratch" {
		t.Errorf("stored host = %q, want wsl://scratch", h)
	}
}

// activeWSLProfile is what `dejima wsl status` uses to say "you're connected
// through this" — it must not claim a WSL connection for a TCP profile.
func TestActiveWSLProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := activeWSLProfile(); got != "" {
		t.Errorf("no profiles yet, got %q", got)
	}
	cfg, _ := clientcfg.Load()
	cfg.Profiles = []clientcfg.Profile{{Name: "mini", Host: "100.64.0.1:7273"}}
	cfg.ActiveProfile = "mini"
	if err := clientcfg.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if got := activeWSLProfile(); got != "" {
		t.Errorf("a TCP profile is not a WSL one, got %q", got)
	}
	if err := saveWSLProfile("dejima"); err != nil {
		t.Fatal(err)
	}
	if got := activeWSLProfile(); got != "wsl" {
		t.Errorf("activeWSLProfile = %q, want wsl", got)
	}
}

// Every `dejima wsl` verb goes through the platform guard before it touches
// wsl.exe. Off Windows that means a clean refusal from each one — `dejima wsl
// start` and `dejima wsl stop` included, since those are the two a user reaches
// for after a reboot and the worst outcome would be an opaque exec failure.
func TestWSLVerbsRefuseOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the non-Windows guard")
	}
	cases := map[string]func() *cobra.Command{
		"dejima wsl start":  newWSLStartCmd,
		"dejima wsl stop":   newWSLStopCmd,
		"dejima wsl status": newWSLStatusCmd,
		"dejima wsl setup":  newWSLSetupCmd,
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := build()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("%s should refuse off Windows", name)
			}
			if !strings.Contains(err.Error(), "Windows-only") {
				t.Errorf("%s error should name the platform limit, got: %v", name, err)
			}
		})
	}
}

// `dejima wsl` with no subcommand shows status, so it must refuse the same way
// rather than printing an empty report.
func TestWSLRootRefusesOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the non-Windows guard")
	}
	cmd := newWSLCmd()
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Windows-only") {
		t.Errorf("bare `dejima wsl` should refuse off Windows, got: %v", err)
	}
}

// Off Windows every `dejima wsl` verb must refuse with an explanation and a
// pointer to the native path, not shell out to a missing wsl.exe.
func TestRequireWSLPlatformOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts the non-Windows guard")
	}
	err := requireWSLPlatform()
	if err == nil {
		t.Fatal("expected a refusal off Windows")
	}
	if !strings.Contains(err.Error(), "Windows-only") {
		t.Errorf("error should say why, got: %v", err)
	}
	if !strings.Contains(err.Error(), "dejima onboard") {
		t.Errorf("error should point at the native path, got: %v", err)
	}
}
