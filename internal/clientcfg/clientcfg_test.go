package clientcfg

import "testing"

func TestActiveHost(t *testing.T) {
	profiles := []Profile{
		{Name: "minion", Host: "100.77.85.107:7273"},
		{Name: "work", Host: "work.tailnet:7273"},
	}
	cases := []struct {
		name      string
		cfg       Config
		wantHost  string
		wantOK    bool
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
