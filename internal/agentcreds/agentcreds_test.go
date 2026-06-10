package agentcreds

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateClaude(t *testing.T) {
	cases := []struct {
		name string
		blob string
		ok   bool
	}{
		{"valid", `{"claudeAiOauth":{"accessToken":"sk-ant-x"}}`, true},
		{"missing key", `{"somethingElse":{}}`, false},
		{"not json", `hello`, false},
		{"empty", ``, false},
		{"json array", `[1,2]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateClaude([]byte(tc.blob))
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestWriteSeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets", "claude")
	blob := []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-x"}}`)

	path, err := WriteSeed(dir, blob)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(blob) {
		t.Fatalf("seed content mismatch: %s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("seed file perm = %o, want 600", perm)
	}

	// Overwrite must replace atomically without leaving temp files behind.
	if _, err := WriteSeed(dir, []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-y"}}`)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the seed file in dir, got %d entries", len(entries))
	}
}
