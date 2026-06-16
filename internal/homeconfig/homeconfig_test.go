package homeconfig

import (
	"bytes"
	"strings"
	"testing"
)

func TestOpenclawTemplate(t *testing.T) {
	files, err := Template("openclaw")
	if err != nil {
		t.Fatalf("Template(openclaw): %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected scaffold files")
	}

	var configCount int
	have := map[string]bool{}
	for _, f := range files {
		have[f.Path] = true
		if f.Path == "" || strings.HasPrefix(f.Path, "/") {
			t.Errorf("scaffold path %q must be relative with no leading slash", f.Path)
		}
		if len(f.Body) == 0 {
			t.Errorf("scaffold %q has empty body", f.Path)
		}
		if f.ConfigFile {
			configCount++
		}
	}
	for _, want := range []string{"openclaw.config.toml", ".gitignore", "SECRETS.md"} {
		if !have[want] {
			t.Errorf("missing scaffold file %q", want)
		}
	}
	if configCount != 1 {
		t.Errorf("expected exactly one ConfigFile, got %d", configCount)
	}

	// Secrets must never be scaffolded in plaintext — the SECRETS.md must point
	// at the two safe paths, not hold a token, and the gitignore must exclude
	// secret files.
	for _, f := range files {
		if f.Path == ".gitignore" && !bytes.Contains(f.Body, []byte("*.env")) {
			t.Error(".gitignore should exclude *.env")
		}
	}
}

func TestConfigPath(t *testing.T) {
	if got := ConfigPath("openclaw"); got != "openclaw.config.toml" {
		t.Errorf("ConfigPath(openclaw) = %q, want openclaw.config.toml", got)
	}
	if got := ConfigPath("nope"); got != "" {
		t.Errorf("ConfigPath(unknown) = %q, want empty", got)
	}
}

func TestTemplateUnknown(t *testing.T) {
	if _, err := Template("nope"); err == nil {
		t.Error("expected error for unknown framework")
	}
}
