package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// assertHostInternalBind serves TWO listeners, and every message inside it used
// to name the token one. A real operator log showed
//
//	WARN "token listener bound to a non-loopback address" addr=172.17.0.1:7280
//
// where 7280 is the egress proxy. Harmless in a warning; the same wrong name was
// in the wildcard ERROR, which would send someone to fix a flag they never set.
func TestBindCheckNamesItsCaller(t *testing.T) {
	for _, tc := range []struct {
		what, flag, addr string
		wantErr          bool
		mustSay          []string
		mustNotSay       []string
	}{
		{
			what: "egress proxy", flag: "--egress-proxy", addr: "0.0.0.0:7280", wantErr: true,
			mustSay:    []string{"--egress-proxy", "egress proxy"},
			mustNotSay: []string{"--token-tcp", "token listener"},
		},
		{
			what: "token listener", flag: "--token-tcp", addr: "0.0.0.0:7274", wantErr: true,
			mustSay:    []string{"--token-tcp", "token listener"},
			mustNotSay: []string{"--egress-proxy"},
		},
		{
			// The bridge-gateway case the operator is running: allowed, warned,
			// and the warning must name the right listener.
			what: "egress proxy", flag: "--egress-proxy", addr: "172.17.0.1:7280",
			mustSay:    []string{"egress proxy", "172.17.0.1:7280"},
			mustNotSay: []string{"token listener"},
		},
		{
			// Loopback: allowed, and must not warn at all.
			what: "token listener", flag: "--token-tcp", addr: "127.0.0.1:7274",
			mustNotSay: []string{"non-loopback"},
		},
	} {
		t.Run(tc.flag+" "+tc.addr, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			err := assertHostInternalBind(log, tc.what, tc.flag, tc.addr)
			if tc.wantErr && err == nil {
				t.Fatal("a wildcard bind was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("a host-internal bind was refused: %v", err)
			}
			combined := buf.String()
			if err != nil {
				combined += err.Error()
			}
			for _, s := range tc.mustSay {
				if !strings.Contains(combined, s) {
					t.Errorf("output never mentions %q:\n%s", s, combined)
				}
			}
			for _, s := range tc.mustNotSay {
				if strings.Contains(combined, s) {
					t.Errorf("output wrongly mentions %q — it names the wrong listener:\n%s", s, combined)
				}
			}
		})
	}
}

// The wildcard refusal is the security property and must survive any rewording.
func TestWildcardBindStillRefused(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for _, addr := range []string{"0.0.0.0:7274", ":::7274", ":7274"} {
		if err := assertHostInternalBind(log, "token listener", "--token-tcp", addr); err == nil {
			t.Errorf("wildcard %q was accepted; the bind is the blast-radius limiter", addr)
		}
	}
}
