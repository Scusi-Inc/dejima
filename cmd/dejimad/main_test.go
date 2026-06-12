package main

import (
	"io"
	"log/slog"
	"testing"
)

func TestAssertHostInternalBind(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		addr    string
		wantErr bool
	}{
		{"127.0.0.1:7274", false},            // loopback — ideal
		{"[::1]:7274", false},                // loopback v6
		{"172.17.0.1:7274", false},           // bridge gateway — allowed but warned
		{"host.docker.internal:7274", false}, // hostname — allowed but warned
		{"0.0.0.0:7274", true},               // wildcard — refused (LAN exposure)
		{":7274", true},                      // empty host == wildcard — refused
		{"[::]:7274", true},                  // v6 wildcard — refused
		{"127.0.0.1", true},                  // missing port — parse error
		{"", true},                           // empty — parse error
	}
	for _, c := range cases {
		err := assertHostInternalBind(log, c.addr)
		if (err != nil) != c.wantErr {
			t.Errorf("assertHostInternalBind(%q) err=%v, wantErr=%v", c.addr, err, c.wantErr)
		}
	}
}
