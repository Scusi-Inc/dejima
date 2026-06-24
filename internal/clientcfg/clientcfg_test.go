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
