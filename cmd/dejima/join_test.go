package main

import (
	"context"
	"io"
	"testing"

	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/invite"
)

// TestCLIJoin runs the teammate side end-to-end (client-only, no daemon): a
// valid invite blob is decoded and persisted as the active connection profile
// carrying the host + token.
func TestCLIJoin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_TOKEN", "") // ensure env doesn't shadow the saved token

	blob, err := invite.Encode(invite.Payload{
		Host: "minion.ts.net:7274", Token: "sek_join", Role: "operator",
		Islands: []string{"webapp"}, Name: "minion",
	})
	if err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"join", blob})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("join: %v", err)
	}

	cfg, err := clientcfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveProfile != "minion" {
		t.Errorf("active profile = %q, want minion", cfg.ActiveProfile)
	}
	if got := cfg.TokenForHost("minion.ts.net:7274"); got != "sek_join" {
		t.Errorf("saved token = %q, want sek_join", got)
	}

	// A garbage blob is a clean error, not a panic.
	bad := newRootCmd()
	bad.SetArgs([]string{"join", "not-an-invite"})
	bad.SetOut(io.Discard)
	bad.SetErr(io.Discard)
	if err := bad.ExecuteContext(context.Background()); err == nil {
		t.Error("join with a bad blob: expected an error, got nil")
	}
}

// TestCLITokenInviteValidation covers the issue side's pre-daemon validation
// (it references the `token invite` command for the coverage gate without
// needing a live daemon): --role and --host are both required.
func TestCLITokenInviteValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := [][]string{
		{"token", "invite"},                       // no --role
		{"token", "invite", "--role", "operator"}, // no --host
		{"token", "invite", "--host", "h:7274"},   // no --role
	}
	for _, args := range cases {
		root := newRootCmd()
		root.SetArgs(args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.ExecuteContext(context.Background()); err == nil {
			t.Errorf("%v: expected a validation error, got nil", args)
		}
	}
}
