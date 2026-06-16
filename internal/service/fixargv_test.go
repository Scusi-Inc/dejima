package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestSupervisionFixArgv(t *testing.T) {
	tests := []struct {
		name string
		sup  Supervision
		want []string
	}{
		{
			name: "no concern → no fix",
			sup:  Supervision{Mode: "launchd-system", RebootDurable: true, Managed: true},
			want: nil,
		},
		{
			name: "systemd enabled but linger off → enable-linger",
			sup:  classifySystemd("active", "enabled", "Linger=no"),
			want: []string{"loginctl", "enable-linger", currentUser()},
		},
		{
			name: "systemd active not enabled → enable unit",
			sup:  classifySystemd("active", "disabled", "Linger=no"),
			want: []string{"systemctl", "--user", "enable", systemdUnitName},
		},
		{
			name: "system plist present not loaded → restart --system",
			sup:  classifyLaunchd(true, false, false, false),
			want: []string{"dejima", "service", "restart", "--system"},
		},
		{
			name: "gui domain (headless concern) → install --system",
			sup:  classifyLaunchd(false, false, true, false),
			want: []string{"dejima", "service", "install", "--system"},
		},
		{
			name: "user domain (per-boot) → install --system",
			sup:  classifyLaunchd(false, false, false, true),
			want: []string{"dejima", "service", "install", "--system"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.sup.FixArgv("dejima")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FixArgv = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSupervisionFixArgvDistinguishesSystemdConcerns(t *testing.T) {
	// The two systemd-user concerns share a Mode; FixArgv must split them by the
	// Concern text, not pick the wrong remediation.
	enabledNoLinger := classifySystemd("active", "enabled", "Linger=no")
	if !strings.Contains(enabledNoLinger.Concern, "enable-linger") {
		t.Fatalf("precondition: concern = %q", enabledNoLinger.Concern)
	}
	if got := enabledNoLinger.FixArgv("dejima"); got[0] != "loginctl" {
		t.Errorf("enabled+no-linger fix = %v, want loginctl enable-linger", got)
	}
}
