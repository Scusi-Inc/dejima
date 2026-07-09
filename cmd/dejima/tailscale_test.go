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

// TestMagicDNSNameForHost: a raw tailnet IP resolves to the owning node's
// MagicDNS name (with the port carried over) by scanning tailscale status.
func TestMagicDNSNameForHost(t *testing.T) {
	orig := tailscaleStatusNodes
	t.Cleanup(func() { tailscaleStatusNodes = orig })
	tailscaleStatusNodes = func() []tailscaleNode {
		return []tailscaleNode{
			{DNSName: "laptop.tail2f808e.ts.net.", TailscaleIPs: []string{"100.85.200.90"}},
			{DNSName: "minion.tail2f808e.ts.net.", TailscaleIPs: []string{"100.77.85.107", "fd7a:115c:a1e0::1"}},
		}
	}
	if got, ok := magicDNSNameForHost("100.77.85.107:7273"); !ok || got != "minion.tail2f808e.ts.net:7273" {
		t.Errorf("magicDNSNameForHost(ip:port) = (%q,%v), want (minion.tail2f808e.ts.net:7273,true)", got, ok)
	}
	if got, ok := magicDNSNameForHost("100.77.85.107"); !ok || got != "minion.tail2f808e.ts.net" {
		t.Errorf("magicDNSNameForHost(ip) = (%q,%v), want (minion.tail2f808e.ts.net,true)", got, ok)
	}
	// An IP no node advertises → not found (don't guess).
	if _, ok := magicDNSNameForHost("100.99.99.99:7273"); ok {
		t.Error("magicDNSNameForHost for an unknown IP should be ok=false")
	}
	// Tailscale unavailable → not found.
	tailscaleStatusNodes = func() []tailscaleNode { return nil }
	if _, ok := magicDNSNameForHost("100.77.85.107:7273"); ok {
		t.Error("magicDNSNameForHost with no tailscale should be ok=false")
	}
}

// TestInviteHostFor: the remote-mint upgrade — an operator connected to a
// daemon by its raw tailnet IP still gets a name-based invite when the name is
// resolvable; local socket falls back to the local daemon's own address.
func TestInviteHostFor(t *testing.T) {
	origNodes, origIP, origFQDN := tailscaleStatusNodes, tailscaleIPLookup, tailscaleFQDNLookup
	t.Cleanup(func() {
		tailscaleStatusNodes, tailscaleIPLookup, tailscaleFQDNLookup = origNodes, origIP, origFQDN
	})
	tailscaleStatusNodes = func() []tailscaleNode {
		return []tailscaleNode{{DNSName: "minion.tail2f808e.ts.net.", TailscaleIPs: []string{"100.77.85.107"}}}
	}

	// Remote, active host is a raw tailnet IP → upgraded to the MagicDNS name.
	if hp, raw, ok := inviteHostFor("100.77.85.107:7273"); !ok || raw || hp != "minion.tail2f808e.ts.net:7273" {
		t.Errorf("inviteHostFor(raw IP) = (%q,%v,%v), want (minion.tail2f808e.ts.net:7273,false,true)", hp, raw, ok)
	}

	// Remote, raw IP with no resolvable name → keep the IP, flag rawIP so the
	// caller warns.
	tailscaleStatusNodes = func() []tailscaleNode { return nil }
	if hp, raw, ok := inviteHostFor("100.77.85.107:7273"); !ok || !raw || hp != "100.77.85.107:7273" {
		t.Errorf("inviteHostFor(unresolvable raw IP) = (%q,%v,%v), want (100.77.85.107:7273,true,true)", hp, raw, ok)
	}

	// Remote, already a MagicDNS name → passed through untouched.
	if hp, raw, ok := inviteHostFor("minion.ts.net:7274"); !ok || raw || hp != "minion.ts.net:7274" {
		t.Errorf("inviteHostFor(name) = (%q,%v,%v), want (minion.ts.net:7274,false,true)", hp, raw, ok)
	}

	// Local socket → detect the local daemon's own address (daemonInviteHost).
	tailscaleFQDNLookup = func() string { return "here.tail2f808e.ts.net" }
	tailscaleIPLookup = func() (string, bool) { return "100.64.0.9", true }
	if hp, _, ok := inviteHostFor(""); !ok || hp != "here.tail2f808e.ts.net:7273" {
		t.Errorf("inviteHostFor(local) = (%q,%v), want (here.tail2f808e.ts.net:7273,true)", hp, ok)
	}
}

// TestOpenTeamViewPrefill: on the local socket the host prefills from the
// captured tailnet address — MagicDNS name preferred, IP as fallback (with the
// default TCP port); connected remotely by IP it upgrades to the MagicDNS name;
// an already-named remote host is passed through.
func TestOpenTeamViewPrefill(t *testing.T) {
	origIP, origFQDN, origNodes := tailscaleIPLookup, tailscaleFQDNLookup, tailscaleStatusNodes
	t.Cleanup(func() {
		tailscaleIPLookup, tailscaleFQDNLookup, tailscaleStatusNodes = origIP, origFQDN, origNodes
	})

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

	// Remote by raw IP → upgraded to the daemon's MagicDNS name.
	tailscaleStatusNodes = func() []tailscaleNode {
		return []tailscaleNode{{DNSName: "minion.tail2f808e.ts.net.", TailscaleIPs: []string{"100.77.85.107"}}}
	}
	m3, _ := tuiModel{activeHost: "100.77.85.107:7273"}.openTeamView()
	if got := m3.(tuiModel).team.host; got != "minion.tail2f808e.ts.net:7273" {
		t.Errorf("remote-by-IP prefill = %q, want the upgraded MagicDNS name", got)
	}

	// Remote, already a name → passed through.
	m4, _ := tuiModel{activeHost: "minion.ts.net:7274"}.openTeamView()
	if got := m4.(tuiModel).team.host; got != "minion.ts.net:7274" {
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
