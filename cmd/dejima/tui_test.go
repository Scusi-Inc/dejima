package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestHostTerminalsAvailable: host terminals are owner-only, so the TUI must
// only poll/show/open them for an owner (or an unknown role, to avoid hiding
// them from an owner on a daemon that didn't stamp a role) — never for a known
// operator/viewer, whose poll would 403 "requires owner" and flash an error.
func TestHostTerminalsAvailable(t *testing.T) {
	on := &api.OverviewResponse{HostTerminalsEnabled: true}
	off := &api.OverviewResponse{HostTerminalsEnabled: false}
	cases := []struct {
		name     string
		overview *api.OverviewResponse
		role     string
		want     bool
	}{
		{"owner + feature on", on, "owner", true},
		{"operator + feature on", on, "operator", false},
		{"viewer + feature on", on, "viewer", false},
		{"unknown role + feature on", on, "", true}, // don't hide from an owner pre-role
		{"owner + feature off", off, "owner", false},
		{"operator + feature off", off, "operator", false},
		{"no overview yet", nil, "owner", false},
	}
	for _, c := range cases {
		m := tuiModel{overview: c.overview, callerRole: c.role}
		if got := m.hostTerminalsAvailable(); got != c.want {
			t.Errorf("%s: hostTerminalsAvailable() = %v, want %v", c.name, got, c.want)
		}
	}
}
