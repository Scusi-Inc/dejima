package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/clientcfg"
)

// seedProfiles writes a client.json with one saved remote profile selected as
// active, into a temp HOME so the on-disk store is isolated per test.
func seedProfiles(t *testing.T) clientcfg.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // redirect ~/.dejima
	cfg := clientcfg.Config{
		Profiles:      []clientcfg.Profile{{Name: "cloud", Host: "100.64.0.9:7273"}},
		ActiveProfile: "cloud",
	}
	if err := clientcfg.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// resetEphemeralFlags clears the process-global launch-flag state so one test's
// `-p`/`--host` can't leak into another (these are package vars bound by cobra).
func resetEphemeralFlags(t *testing.T) {
	t.Helper()
	prevP, prevH := flagProfile, flagHost
	flagProfile, flagHost = "", ""
	t.Cleanup(func() { flagProfile, flagHost = prevP, prevH })
}

// readClientJSON returns the raw bytes of client.json, or nil if absent — used
// to assert byte-for-byte that an ephemeral `-p` wrote nothing.
func readClientJSON(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(os.Getenv("HOME") + "/.dejima/client.json")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return b
}

// The anti-stomp guarantee: selecting a profile with the ephemeral `-p` flag
// resolves the target for this process but must NOT mutate client.json —
// otherwise one TUI launch could overwrite the active_profile another shares.
func TestEphemeralProfileDoesNotPersist(t *testing.T) {
	seedProfiles(t)
	resetEphemeralFlags(t)
	before := readClientJSON(t)

	// `-p cloud` — different from the active profile only in that it must not
	// be written. (Here the active profile happens to be "cloud" too; the point
	// is no write occurs at all, regardless of value.)
	flagProfile = "cloud"
	host, label, ok := ephemeralTarget()
	if !ok || host != "100.64.0.9:7273" || label != "cloud" {
		t.Fatalf("ephemeralTarget() = (%q, %q, %v), want (100.64.0.9:7273, cloud, true)", host, label, ok)
	}
	// resolveTarget must report the flag-sourced target and likewise not persist.
	if h, _, src := resolveTarget(); h != "100.64.0.9:7273" || src != "flag" {
		t.Fatalf("resolveTarget() = (%q, src=%q), want (100.64.0.9:7273, flag)", h, src)
	}

	if after := readClientJSON(t); string(after) != string(before) {
		t.Fatalf("client.json was mutated by -p:\nbefore: %s\nafter:  %s", before, after)
	}
	// And the on-disk active profile is untouched.
	cfg, _ := clientcfg.Load()
	if cfg.ActiveProfile != "cloud" {
		t.Fatalf("active_profile changed to %q, want cloud (unchanged)", cfg.ActiveProfile)
	}
}

// --host is the headless sibling of -p: a literal address for one command,
// also non-persistent, and it wins over -p when both are set.
func TestEphemeralHostFlag(t *testing.T) {
	seedProfiles(t)
	resetEphemeralFlags(t)
	before := readClientJSON(t)

	flagHost = "minion" // bare host → default port appended
	flagProfile = "cloud"
	host, _, ok := ephemeralTarget()
	if !ok || host != "minion:7273" {
		t.Fatalf("--host should win and normalize, got (%q, %v)", host, ok)
	}
	if after := readClientJSON(t); string(after) != string(before) {
		t.Fatalf("--host mutated client.json:\nbefore: %s\nafter: %s", before, after)
	}
}

// No launch flag → ephemeralTarget reports not-ok so resolveTarget's normal
// env/profile/local precedence stays in force.
func TestEphemeralTargetUnsetIsTransparent(t *testing.T) {
	resetEphemeralFlags(t)
	if _, _, ok := ephemeralTarget(); ok {
		t.Fatal("ephemeralTarget() should be ok=false when no flag is set")
	}
}

// End-to-end through the cobra tree: `dejima profile add` saves a profile and
// `dejima profile ls` lists it with the active marker. Exercises the real CLI
// wiring, not just the store.
func TestProfileAddAndLsCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetEphemeralFlags(t)

	root := newRootCmd()
	root.SetArgs([]string{"profile", "add", "cloud", "100.64.0.9:7273"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("profile add: %v", err)
	}
	cfg, _ := clientcfg.Load()
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Host != "100.64.0.9:7273" {
		t.Fatalf("add did not persist profile: %+v", cfg.Profiles)
	}

	var out bytes.Buffer
	root = newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"profile", "ls"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("profile ls: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "cloud") || !strings.Contains(got, "100.64.0.9:7273") {
		t.Fatalf("profile ls output missing the added profile:\n%s", got)
	}
	if !strings.Contains(got, "local") {
		t.Fatalf("profile ls should always list the synthetic local target:\n%s", got)
	}
}

// `dejima profile switch` IS the persistent path — unlike -p, it writes
// active_profile (parity with the TUI switcher).
//
// The store-level assertions below are the substance; this first half drives the
// actual cobra verb, like its `add` / `ls` / `rm` siblings do. It was missing,
// and the coverage gate did not notice because the sentence above this function
// names the command — prose was standing in for the verb nobody invoked.
func TestSwitchProfileCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetEphemeralFlags(t)
	if err := clientcfg.Save(clientcfg.Config{
		Profiles: []clientcfg.Profile{{Name: "cloud", Host: "100.64.0.9:7273"}},
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"profile", "switch", "cloud"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("profile switch: %v", err)
	}
	if cfg, _ := clientcfg.Load(); cfg.ActiveProfile != "cloud" {
		t.Fatalf("`profile switch cloud` did not persist active_profile, got %q", cfg.ActiveProfile)
	}
	if got := out.String(); !strings.Contains(got, "cloud") {
		t.Errorf("switch should say what it switched to; got %q", got)
	}

	// An unknown name is rejected through the command, not just the store — the
	// exit status is what a script keys off.
	root = newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"profile", "switch", "nope"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Error("`profile switch nope` should fail — no such profile")
	}
}

func TestSwitchProfilePersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := clientcfg.Save(clientcfg.Config{
		Profiles: []clientcfg.Profile{{Name: "cloud", Host: "100.64.0.9:7273"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := clientcfg.SwitchProfile("cloud"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := clientcfg.Load()
	if cfg.ActiveProfile != "cloud" {
		t.Fatalf("switch did not persist active_profile, got %q", cfg.ActiveProfile)
	}
	// "local" clears it back to the socket.
	if err := clientcfg.SwitchProfile("local"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = clientcfg.Load()
	if cfg.ActiveProfile != "" {
		t.Fatalf(`switch local should clear active_profile, got %q`, cfg.ActiveProfile)
	}
	// Switching to an unknown profile is rejected (no dangling reference).
	if err := clientcfg.SwitchProfile("nope"); err == nil {
		t.Fatal("switch to unknown profile should error")
	}
}

// End-to-end through the cobra tree: `dejima profile rm` deletes a saved profile
// (CLI parity with the TUI switcher's [d]). The freshness-gate reference is the
// SetArgs line, not this sentence — the gate reads code now.
func TestProfileRmCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetEphemeralFlags(t)
	if err := clientcfg.Save(clientcfg.Config{
		Profiles:      []clientcfg.Profile{{Name: "cloud", Host: "100.64.0.9:7273"}},
		ActiveProfile: "cloud",
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"profile", "rm", "cloud"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("profile rm: %v", err)
	}
	cfg, _ := clientcfg.Load()
	if len(cfg.Profiles) != 0 {
		t.Fatalf("rm did not delete the profile: %+v", cfg.Profiles)
	}
	if cfg.ActiveProfile != "" {
		t.Fatalf("rm of the active profile should clear active_profile, got %q", cfg.ActiveProfile)
	}
	if !strings.Contains(out.String(), "removed profile") {
		t.Fatalf("rm output missing confirmation:\n%s", out.String())
	}

	// Removing an unknown profile is a loud error.
	root = newRootCmd()
	root.SetArgs([]string{"profile", "rm", "nope"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("rm of an unknown profile should error")
	}
}
