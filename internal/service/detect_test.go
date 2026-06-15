package service

import "testing"

func TestClassifySystemd(t *testing.T) {
	cases := []struct {
		name                     string
		active, enabled, linger  string
		wantMode                 string
		wantManaged, wantDurable bool
		wantConcern              bool
	}{
		{"durable", "active", "enabled", "yes", "systemd-user", true, true, false},
		{"enabled-no-linger", "active", "enabled", "no", "systemd-user", true, false, true},
		{"active-not-enabled", "active", "disabled", "no", "systemd-user", true, false, true},
		{"absent", "inactive", "disabled", "no", "none", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := classifySystemd(tc.active, tc.enabled, tc.linger)
			if s.Mode != tc.wantMode || s.Managed != tc.wantManaged || s.RebootDurable != tc.wantDurable {
				t.Errorf("got mode=%q managed=%v durable=%v, want mode=%q managed=%v durable=%v",
					s.Mode, s.Managed, s.RebootDurable, tc.wantMode, tc.wantManaged, tc.wantDurable)
			}
			if (s.Concern != "") != tc.wantConcern {
				t.Errorf("concern=%q, wantConcern=%v", s.Concern, tc.wantConcern)
			}
		})
	}
}

func TestClassifyLaunchd(t *testing.T) {
	cases := []struct {
		name                                  string
		systemPlist, systemLoaded, gui, user  bool
		wantMode                              string
		wantManaged, wantDurable, wantConcern bool
	}{
		{"system-loaded", true, true, false, false, "launchd-system", true, true, false},
		{"system-plist-not-loaded", true, false, false, false, "launchd-system", false, false, true},
		{"gui-agent", false, false, true, false, "launchd-gui", true, false, true},
		{"user-agent", false, false, false, true, "launchd-user", true, false, true},
		{"none", false, false, false, false, "none", false, false, false},
		// System domain wins even if a stale gui agent also lingers.
		{"system-precedence", true, true, true, false, "launchd-system", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := classifyLaunchd(tc.systemPlist, tc.systemLoaded, tc.gui, tc.user)
			if s.Mode != tc.wantMode || s.Managed != tc.wantManaged || s.RebootDurable != tc.wantDurable {
				t.Errorf("got mode=%q managed=%v durable=%v, want mode=%q managed=%v durable=%v",
					s.Mode, s.Managed, s.RebootDurable, tc.wantMode, tc.wantManaged, tc.wantDurable)
			}
			if (s.Concern != "") != tc.wantConcern {
				t.Errorf("concern=%q, wantConcern=%v", s.Concern, tc.wantConcern)
			}
		})
	}
}
