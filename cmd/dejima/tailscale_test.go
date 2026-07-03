package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func TestIsTailscaleHost(t *testing.T) {
	cases := map[string]bool{
		"100.77.85.107:7273":       true,  // the live-failure address (CGNAT)
		"100.64.0.1":               true,  // bottom of 100.64.0.0/10
		"100.127.255.255:7273":     true,  // top of /10
		"100.128.0.1":              false, // just outside /10
		"100.63.0.1":               false, // just below /10
		"minion.ts.net:7274":       true,  // MagicDNS
		"MINION.TS.NET":            true,  // case-insensitive
		"example.com:7273":         false, // ordinary DNS name — don't over-warn
		"192.168.1.5:7273":         false, // LAN
		"10.0.0.1":                 false, // RFC1918
		"[fd7a:115c:a1e0::1]:7273": true,  // Tailscale IPv6 ULA
		"fd7a:115c:a1e0::1":        true,
		"fd00::1":                  false, // other ULA
		"":                         false,
	}
	for host, want := range cases {
		if got := isTailscaleHost(host); got != want {
			t.Errorf("isTailscaleHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"100.77.85.107:7273":       "100.77.85.107",
		"minion.ts.net:7274":       "minion.ts.net",
		"[fd7a:115c:a1e0::1]:7273": "fd7a:115c:a1e0::1",
		"bare-host":                "bare-host",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTailscaleIPv4(t *testing.T) {
	if ip, ok := parseTailscaleIPv4("100.77.85.107\n"); !ok || ip != "100.77.85.107" {
		t.Errorf("single v4 = (%q,%v), want (100.77.85.107,true)", ip, ok)
	}
	// v4 + v6 lines (what `tailscale ip` prints without -4): take the v4.
	if ip, ok := parseTailscaleIPv4("100.77.85.107\nfd7a:115c:a1e0::1\n"); !ok || ip != "100.77.85.107" {
		t.Errorf("mixed = (%q,%v), want the v4", ip, ok)
	}
	// A non-Tailscale v4 (shouldn't happen from `tailscale ip`, but be strict).
	if _, ok := parseTailscaleIPv4("192.168.1.5\n"); ok {
		t.Error("a non-Tailscale address must not be accepted")
	}
	if _, ok := parseTailscaleIPv4(""); ok {
		t.Error("empty output → no IP")
	}
}

// TestOpenTeamViewPrefill: on the local socket the host prefills from the
// captured tailnet IP (with the default TCP port); connected remotely, the
// active host wins and no lookup happens.
func TestOpenTeamViewPrefill(t *testing.T) {
	orig := tailscaleIPLookup
	t.Cleanup(func() { tailscaleIPLookup = orig })

	// Local socket → prefill "<tailnet-ip>:7273".
	tailscaleIPLookup = func() (string, bool) { return "100.77.85.107", true }
	m2, _ := tuiModel{activeHost: ""}.openTeamView()
	if got := m2.(tuiModel).team.host; got != "100.77.85.107:7273" {
		t.Errorf("local-socket prefill = %q, want 100.77.85.107:7273", got)
	}

	// Remote → activeHost wins; the lookup must not even run.
	tailscaleIPLookup = func() (string, bool) { t.Fatal("must not look up tailnet IP when already remote"); return "", false }
	m3, _ := tuiModel{activeHost: "minion.ts.net:7274"}.openTeamView()
	if got := m3.(tuiModel).team.host; got != "minion.ts.net:7274" {
		t.Errorf("remote prefill = %q, want the active host", got)
	}
}

// TestRenderMintedInvitePanelTailscale: the prereq line appears only when the
// daemon host is a Tailscale address (a3 #214.1: "only show it when the host is
// actually a 100.64/10 addr").
func TestRenderMintedInvitePanelTailscale(t *testing.T) {
	mk := func(host string) string {
		m := tuiModel{team: &teamView{
			host:       host,
			mintedBlob: "dejima-invite:abc123",
			minted:     &api.CreateTokenResponse{Token: api.TokenView{Role: "operator", Label: "alice"}},
		}}
		return m.renderMintedInvite()
	}
	if out := mk("100.77.85.107:7273"); !strings.Contains(out, "on Tailscale") {
		t.Errorf("Tailscale host should show the tailnet prereq line\n---\n%s", out)
	}
	if out := mk("mini.local:7273"); strings.Contains(out, "on Tailscale") {
		t.Errorf("non-Tailscale host must NOT show the tailnet prereq line\n---\n%s", out)
	}
}
