package main

import (
	"net"
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

func TestTCPReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if !tcpReachable(addr) {
		t.Errorf("open listener %s should be reachable", addr)
	}
	_ = ln.Close()
	if tcpReachable(addr) {
		t.Errorf("closed listener %s should not be reachable", addr)
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

// TestDaemonInviteHost: the MagicDNS name is preferred over the raw tailnet IP
// (stable across tailnets), with the IP as the fallback when MagicDNS is off.
func TestDaemonInviteHost(t *testing.T) {
	origIP, origFQDN := tailscaleIPLookup, tailscaleFQDNLookup
	t.Cleanup(func() { tailscaleIPLookup, tailscaleFQDNLookup = origIP, origFQDN })

	// MagicDNS name available → prefer it, rawIP=false.
	tailscaleFQDNLookup = func() string { return "minion.tail2f808e.ts.net" }
	tailscaleIPLookup = func() (string, bool) { return "100.77.85.107", true }
	if hp, raw, ok := daemonInviteHost(); !ok || raw || hp != "minion.tail2f808e.ts.net:7273" {
		t.Errorf("daemonInviteHost with FQDN = (%q,%v,%v), want (minion.tail2f808e.ts.net:7273,false,true)", hp, raw, ok)
	}

	// No MagicDNS → fall back to the raw IP, rawIP=true so callers can warn.
	tailscaleFQDNLookup = func() string { return "" }
	if hp, raw, ok := daemonInviteHost(); !ok || !raw || hp != "100.77.85.107:7273" {
		t.Errorf("daemonInviteHost IP-only = (%q,%v,%v), want (100.77.85.107:7273,true,true)", hp, raw, ok)
	}

	// Neither → not ok.
	tailscaleIPLookup = func() (string, bool) { return "", false }
	if _, _, ok := daemonInviteHost(); ok {
		t.Error("daemonInviteHost with no tailnet address should return ok=false")
	}
}

func TestIsRawTailscaleIP(t *testing.T) {
	cases := map[string]bool{
		"100.77.85.107:7273":       true,  // raw CGNAT IP — the fragile form
		"100.64.0.1":               true,  // raw, no port
		"[fd7a:115c:a1e0::1]:7273": true,  // raw Tailscale IPv6
		"minion.ts.net:7273":       false, // MagicDNS name — the robust form
		"example.com:7273":         false, // not a tailnet host at all
		"192.168.1.5:7273":         false, // LAN
		"":                         false,
	}
	for host, want := range cases {
		if got := isRawTailscaleIP(host); got != want {
			t.Errorf("isRawTailscaleIP(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestOpenTeamViewPrefill: on the local socket the host prefills from the
// captured tailnet address — MagicDNS name preferred, IP as fallback (with the
// default TCP port); connected remotely, the active host wins and no lookup
// happens.
func TestOpenTeamViewPrefill(t *testing.T) {
	origIP, origFQDN := tailscaleIPLookup, tailscaleFQDNLookup
	t.Cleanup(func() { tailscaleIPLookup, tailscaleFQDNLookup = origIP, origFQDN })

	// Local socket, MagicDNS available → prefill the name.
	tailscaleFQDNLookup = func() string { return "minion.tail2f808e.ts.net" }
	tailscaleIPLookup = func() (string, bool) {
		t.Fatal("must not fall back to the IP when MagicDNS is available")
		return "", false
	}
	m1, _ := tuiModel{activeHost: ""}.openTeamView()
	if got := m1.(tuiModel).team.host; got != "minion.tail2f808e.ts.net:7273" {
		t.Errorf("local-socket prefill (MagicDNS) = %q, want minion.tail2f808e.ts.net:7273", got)
	}

	// Local socket, no MagicDNS → prefill "<tailnet-ip>:7273".
	tailscaleFQDNLookup = func() string { return "" }
	tailscaleIPLookup = func() (string, bool) { return "100.77.85.107", true }
	m2, _ := tuiModel{activeHost: ""}.openTeamView()
	if got := m2.(tuiModel).team.host; got != "100.77.85.107:7273" {
		t.Errorf("local-socket prefill (IP fallback) = %q, want 100.77.85.107:7273", got)
	}

	// Remote → activeHost wins; neither lookup runs.
	tailscaleFQDNLookup = func() string { t.Fatal("must not look up MagicDNS when already remote"); return "" }
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
