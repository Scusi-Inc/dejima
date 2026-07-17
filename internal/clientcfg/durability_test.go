package clientcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAtomicAndBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, _ := configPath()

	if err := Save(Config{ActiveProfile: "one", Profiles: []Profile{{Name: "one", Host: "h1:7273"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
		t.Error("first save (no prior config) should not create a .bak")
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temp file must be renamed away, not left behind")
	}

	if err := Save(Config{ActiveProfile: "two", Profiles: []Profile{{Name: "two", Host: "h2:7273"}}}); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p + ".bak")
	if err != nil {
		t.Fatalf("the second save should back up the prior config: %v", err)
	}
	var bc Config
	_ = json.Unmarshal(bak, &bc)
	if bc.ActiveProfile != "one" {
		t.Errorf(".bak should hold the previous (valid) config, got %q", bc.ActiveProfile)
	}
	if cur, _ := Load(); cur.ActiveProfile != "two" {
		t.Errorf("live config should be the latest save, got %q", cur.ActiveProfile)
	}
}

func TestLoadRecoversFromBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, _ := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	good := Config{ActiveProfile: "keep", Profiles: []Profile{{Name: "keep", Host: "h:7273"}}}
	data, _ := json.MarshalIndent(good, "", "  ")
	// A valid backup + a corrupt live config = the botched-write lockout scenario.
	os.WriteFile(p+".bak", data, 0o600)
	os.WriteFile(p, []byte("{ this is not valid json"), 0o600)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load should recover from .bak, got err: %v", err)
	}
	if got.ActiveProfile != "keep" {
		t.Errorf("recovered config = %q, want keep", got.ActiveProfile)
	}
	// And it should have restored client.json so the next read is clean too.
	if reread, err := Load(); err != nil || reread.ActiveProfile != "keep" {
		t.Errorf("Load should restore client.json from .bak (reread=%+v err=%v)", reread, err)
	}
}

func TestLoadErrorsWhenBothCorrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, _ := configPath()
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("{bad"), 0o600)
	os.WriteFile(p+".bak", []byte("also not json"), 0o600)
	if _, err := Load(); err == nil {
		t.Error("Load must ERROR (not silently return empty) when both client.json and .bak are corrupt")
	}
}

func TestLoadMissingIsClean(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if c, err := Load(); err != nil || c.ActiveProfile != "" {
		t.Errorf("a missing config should be (zero, nil), got (%+v, %v)", c, err)
	}
}
