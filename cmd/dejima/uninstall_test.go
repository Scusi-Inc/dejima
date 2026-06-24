package main

import (
	"strings"
	"testing"
)

// TestResolveUninstallMode is the heart of Lane B: bare `uninstall` must refuse
// (no destructive default), the two explicit modes map correctly, --keep-data is
// a non-lying alias for --keep-islands, and contradictory flags are rejected.
func TestResolveUninstallMode(t *testing.T) {
	cases := []struct {
		name        string
		keepIslands bool
		purgeAll    bool
		keepData    bool
		wantMode    uninstallMode
		wantErr     string // substring of the error, "" means no error
	}{
		{
			name:    "bare uninstall refuses with no destructive default",
			wantErr: "refusing to uninstall without an explicit choice",
		},
		{
			name:        "keep-islands",
			keepIslands: true,
			wantMode:    uninstallModeKeepIslands,
		},
		{
			name:     "purge-all",
			purgeAll: true,
			wantMode: uninstallModePurgeAll,
		},
		{
			// --keep-data used to keep config but still destroy volumes — a flag
			// that lied. It now maps to keep-islands (volumes + config survive).
			name:     "keep-data aliases keep-islands and no longer lies",
			keepData: true,
			wantMode: uninstallModeKeepIslands,
		},
		{
			name:        "keep-islands + purge-all is contradictory",
			keepIslands: true,
			purgeAll:    true,
			wantErr:     "mutually exclusive",
		},
		{
			name:     "keep-data + purge-all is contradictory",
			keepData: true,
			purgeAll: true,
			wantErr:  "mutually exclusive",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, err := resolveUninstallMode(tc.keepIslands, tc.purgeAll, tc.keepData)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("got nil error, want substring %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
				}
				if mode != uninstallModeUnset {
					t.Errorf("error path returned mode %v, want uninstallModeUnset", mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tc.wantMode {
				t.Errorf("mode = %v, want %v", mode, tc.wantMode)
			}
		})
	}
}

// TestBareUninstallErrorNamesBothChoices makes sure the refusal message actually
// teaches the operator both escape hatches — a refusal that doesn't say what to
// do instead is a worse default than the old one.
func TestBareUninstallErrorNamesBothChoices(t *testing.T) {
	_, err := resolveUninstallMode(false, false, false)
	if err == nil {
		t.Fatal("bare uninstall must error")
	}
	msg := err.Error()
	for _, want := range []string{"--keep-islands", "--purge-all", "re-adopt", "irreversible"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message missing %q:\n%s", want, msg)
		}
	}
}
