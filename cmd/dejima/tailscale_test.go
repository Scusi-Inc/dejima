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
