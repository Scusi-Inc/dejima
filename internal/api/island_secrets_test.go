package api

import (
	"os"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/secrets"
)

// The encoder is one half of the injection guarantee (the in-island parser is
// the other). Its contract is narrow on purpose: escape ONLY what would break
// line-based parsing, and leave everything else alone — because nothing
// evaluates this file, so there is no quoting to get right.
func TestEncodeSecretsFileLeavesShellMetacharactersAlone(t *testing.T) {
	out := encodeSecretsFile(map[string]string{
		"EXPO_TOKEN": "tok-abc",
		"EVIL":       "$(rm -rf /)`whoami`",
		"QUOTED":     `he said "hi" and 'bye'`,
		"SPACED":     "a b   c",
	})

	// A value with command substitution must survive VERBATIM. If the encoder
	// ever started quoting or stripping these, the parser's guarantee would be
	// silently doing the work twice — or not at all.
	if !strings.Contains(out, "EVIL=$(rm -rf /)`whoami`") {
		t.Errorf("shell metacharacters were altered; encoder should pass them through:\n%s", out)
	}
	if !strings.Contains(out, `QUOTED=he said "hi" and 'bye'`) {
		t.Errorf("quotes were altered:\n%s", out)
	}
	if !strings.Contains(out, "SPACED=a b   c") {
		t.Errorf("spacing was altered:\n%s", out)
	}
}

// Newlines are the one thing that genuinely breaks a line-based format, so they
// are escaped — and '%' with them, since it is the escape character.
func TestEncodeSecretsFileEscapesNewlinesAndPercent(t *testing.T) {
	out := encodeSecretsFile(map[string]string{
		"MULTI":   "line1\nline2",
		"PERCENT": "100%sure",
		"CRLF":    "a\r\nb",
	})
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "MULTI=") && line != "MULTI=line1%0Aline2" {
			t.Errorf("MULTI line = %q", line)
		}
		if strings.HasPrefix(line, "PERCENT=") && line != "PERCENT=100%25sure" {
			t.Errorf("PERCENT line = %q", line)
		}
	}
	// One record per secret: a raw newline would create a phantom entry.
	records := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			records++
		}
	}
	if records != 3 {
		t.Errorf("got %d records, want 3 — a value's newline leaked into the format:\n%s", records, out)
	}
}

// Stable ordering keeps the mount from being rewritten (and the file's mtime
// churning) when nothing actually changed.
func TestEncodeSecretsFileIsSorted(t *testing.T) {
	vals := map[string]string{"ZULU": "z", "ALPHA": "a", "MIKE": "m"}
	first := encodeSecretsFile(vals)
	if first != encodeSecretsFile(vals) {
		t.Error("encoding is not deterministic")
	}
	ai, mi, zi := strings.Index(first, "ALPHA="), strings.Index(first, "MIKE="), strings.Index(first, "ZULU=")
	if !(ai < mi && mi < zi) {
		t.Errorf("records are not name-sorted:\n%s", first)
	}
}

func TestMaterializeWritesAndRemoves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(secrets.BackendEnvVar, "file")
	store, err := secrets.OpenIsland()
	if err != nil {
		t.Fatal(err)
	}

	// No secrets → no file, so nothing is mounted.
	path, err := materializeIslandSecrets(store, "wildfire")
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("island with no secrets produced a file at %q", path)
	}

	if _, err := store.Set("wildfire", "EXPO_TOKEN", "tok-abc", "aoos"); err != nil {
		t.Fatal(err)
	}
	path, err = materializeIslandSecrets(store, "wildfire")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("no file written for an island that has a secret")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "EXPO_TOKEN=tok-abc") {
		t.Errorf("file missing the secret:\n%s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets file mode = %o, want 0600", fi.Mode().Perm())
	}

	// Removing the last secret must delete the file. A stale one would keep
	// injecting a secret the operator believes they deleted.
	if err := store.Remove("wildfire", "EXPO_TOKEN"); err != nil {
		t.Fatal(err)
	}
	path, err = materializeIslandSecrets(store, "wildfire")
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("file still reported after the last secret was removed: %q", path)
	}
	if p, _ := islandSecretsFile("wildfire"); fileExists(p) {
		t.Error("stale secrets file left on disk — it would keep injecting a deleted secret")
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
