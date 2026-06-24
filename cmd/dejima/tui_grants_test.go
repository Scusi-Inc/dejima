package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/link"
)

func grantsModelWith(resp *api.IslandGrantsResponse) tuiModel {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 30
	m.grants = &grantsView{island: "myrepo", resp: resp}
	return m
}

// TestGrantsFullyContained: when nothing is granted, the view says so loudly —
// the locked-down default must be glanceable, not an ambiguous blank.
func TestGrantsFullyContained(t *testing.T) {
	bare := plain(grantsModelWith(&api.IslandGrantsResponse{}).renderGrantsView())
	if !strings.Contains(bare, "fully contained") {
		t.Errorf("empty grants should read as fully contained:\n%s", bare)
	}
}

// TestGrantsRenders: each granted category shows up, with its section header,
// and the rw mode is surfaced.
func TestGrantsRenders(t *testing.T) {
	resp := &api.IslandGrantsResponse{
		Port: []api.PortScopeView{{Name: "vault", HostPath: "/srv/vault", Mode: "rw"}},
		MCP:  []api.MCPGrantView{{Server: "github"}},
		Links: []link.Grant{
			{From: "myrepo", To: "infra", Topic: "deploys"},
		},
		Capability: []api.CapabilityGrantView{{Target: "shortcuts"}},
	}
	bare := plain(grantsModelWith(resp).renderGrantsView())
	for _, want := range []string{
		"Host files (Port)", "vault", "/srv/vault", "rw",
		"MCP servers", "github",
		"Inter-island links", "myrepo → infra", "deploys",
		"Capabilities", "shortcuts",
		"Port 1 · MCP 1 · Links 1 · Capabilities 1",
	} {
		if !strings.Contains(bare, want) {
			t.Errorf("grants view missing %q:\n%s", want, bare)
		}
	}
}

// TestGrantsPartialDenyAll: a category with no grants is labeled deny-all, so a
// half-granted island still reads clearly.
func TestGrantsPartialDenyAll(t *testing.T) {
	resp := &api.IslandGrantsResponse{MCP: []api.MCPGrantView{{Server: "github"}}}
	bare := plain(grantsModelWith(resp).renderGrantsView())
	if !strings.Contains(bare, "none — deny-all") {
		t.Errorf("empty categories should be labeled deny-all:\n%s", bare)
	}
	if !strings.Contains(bare, "github") {
		t.Errorf("the one granted server should still show:\n%s", bare)
	}
}

// TestGrantsKeyClose: esc/q/T close the trust pane.
func TestGrantsKeyClose(t *testing.T) {
	for _, key := range []string{"esc", "q", "T"} {
		m := grantsModelWith(&api.IslandGrantsResponse{})
		var msg tea.KeyMsg
		if key == "esc" {
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		out, _ := m.grantsKey(msg)
		if out.(tuiModel).grants != nil {
			t.Errorf("%q should close the grants view", key)
		}
	}
}

// TestGrantsOpenNoIsland: opening with a blank island is a no-op (no panic, no
// empty pane).
func TestGrantsOpenNoIsland(t *testing.T) {
	m := initialTUIModel(nil)
	out, cmd := m.openGrantsView("")
	if out.(tuiModel).grants != nil || cmd != nil {
		t.Error("opening grants with no island should be a no-op")
	}
}
