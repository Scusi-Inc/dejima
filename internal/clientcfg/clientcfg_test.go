package clientcfg

import (
	"strings"
	"testing"
)

func TestLookupProfile(t *testing.T) {
	cfg := Config{Profiles: []Profile{
		{Name: "cloud", Host: "100.64.0.9:7273"},
		{Name: "localish", Host: ""},
	}}
	cases := []struct {
		name     string
		wantHost string
		wantErr  bool
	}{
		{"cloud", "100.64.0.9:7273", false},
		{"local", "", false}, // synthetic name → socket
		{"", "", false},      // empty → socket
		{"localish", "", false},
		{"missing", "", true}, // explicit typo fails loudly
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, err := cfg.LookupProfile(tc.name)
			if (err != nil) != tc.wantErr {
				t.Fatalf("LookupProfile(%q) err = %v, wantErr %v", tc.name, err, tc.wantErr)
			}
			if host != tc.wantHost {
				t.Fatalf("LookupProfile(%q) host = %q, want %q", tc.name, host, tc.wantHost)
			}
		})
	}
}

func TestAddProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AddProfile("cloud", "100.64.0.9:7273"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load()
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "cloud" {
		t.Fatalf("profile not saved: %+v", cfg.Profiles)
	}
	// Duplicate name is rejected so it can't shadow the first in lookups.
	if err := AddProfile("cloud", "other:7273"); err == nil {
		t.Fatal("duplicate profile name should error")
	}
	// "local" is reserved.
	if err := AddProfile("local", "x:7273"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("adding reserved name should error, got %v", err)
	}
}

func TestRemoveProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AddProfile("cloud", "100.64.0.9:7273"); err != nil {
		t.Fatal(err)
	}
	if err := AddProfile("work", "work.tailnet:7273"); err != nil {
		t.Fatal(err)
	}
	if err := SwitchProfile("cloud"); err != nil {
		t.Fatal(err)
	}

	// Removing the ACTIVE profile clears the active selection (back to local).
	if err := RemoveProfile("cloud"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load()
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "work" {
		t.Fatalf("cloud not removed / work not kept: %+v", cfg.Profiles)
	}
	if cfg.ActiveProfile != "" {
		t.Errorf("active profile should clear when the active one is removed, got %q", cfg.ActiveProfile)
	}

	// Removing a non-active profile leaves the active selection alone.
	if err := SwitchProfile("work"); err != nil {
		t.Fatal(err)
	}
	if err := AddProfile("staging", "stg:7273"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProfile("staging"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load()
	if cfg.ActiveProfile != "work" {
		t.Errorf("active profile should be untouched removing a different one, got %q", cfg.ActiveProfile)
	}

	// Unknown name errors; "local" is reserved.
	if err := RemoveProfile("nope"); err == nil {
		t.Error("removing an unknown profile should error")
	}
	if err := RemoveProfile("local"); err == nil {
		t.Error("removing the reserved local target should error")
	}
}

func TestActiveHost(t *testing.T) {
	profiles := []Profile{
		{Name: "minion", Host: "100.77.85.107:7273"},
		{Name: "work", Host: "work.tailnet:7273"},
	}
	cases := []struct {
		name     string
		cfg      Config
		wantHost string
		wantOK   bool
	}{
		{
			name:     "resolves active profile to its host",
			cfg:      Config{Profiles: profiles, ActiveProfile: "minion"},
			wantHost: "100.77.85.107:7273", wantOK: true,
		},
		{
			name:     "unset active profile is local",
			cfg:      Config{Profiles: profiles},
			wantHost: "", wantOK: false,
		},
		{
			name:     "explicit local is local",
			cfg:      Config{Profiles: profiles, ActiveProfile: "local"},
			wantHost: "", wantOK: false,
		},
		{
			// The exact burnout failure: active_profile names a profile that
			// isn't in the list (it was deleted while still selected). Must
			// fall back to local, not wedge on an unlookup-able target.
			name:     "dangling active profile falls back to local",
			cfg:      Config{Profiles: profiles, ActiveProfile: "gone"},
			wantHost: "", wantOK: false,
		},
		{
			name:     "dangling with no profiles at all",
			cfg:      Config{ActiveProfile: "minion"},
			wantHost: "", wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, ok := tc.cfg.ActiveHost()
			if host != tc.wantHost || ok != tc.wantOK {
				t.Fatalf("ActiveHost() = (%q, %v), want (%q, %v)", host, ok, tc.wantHost, tc.wantOK)
			}
		})
	}
}

func TestRenameProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AddProfile("minon", "100.64.0.9:7273"); err != nil { // typo to fix
		t.Fatal(err)
	}
	if err := AddProfile("work", "work.tailnet:7273"); err != nil {
		t.Fatal(err)
	}
	if err := SwitchProfile("minon"); err != nil {
		t.Fatal(err)
	}

	// Rename keeps the host + follows into ActiveProfile.
	if err := RenameProfile("minon", "minion"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	cfg, _ := Load()
	if h, err := cfg.LookupProfile("minion"); err != nil || h != "100.64.0.9:7273" {
		t.Errorf("renamed profile host = %q err=%v, want the original host", h, err)
	}
	if _, err := cfg.LookupProfile("minon"); err == nil {
		t.Error("old name should no longer resolve")
	}
	if cfg.ActiveProfile != "minion" {
		t.Errorf("ActiveProfile = %q, want it to follow the rename", cfg.ActiveProfile)
	}

	// Collisions and reserved/unknown names are rejected.
	if err := RenameProfile("minion", "work"); err == nil {
		t.Error("renaming onto an existing name should error")
	}
	if err := RenameProfile("minion", "local"); err == nil {
		t.Error("renaming to the reserved 'local' should error")
	}
	if err := RenameProfile("nope", "x"); err == nil {
		t.Error("renaming an unknown profile should error")
	}
}
