package porttoken

import (
	"os"
	"path/filepath"
	"testing"
)

// Asking whether an island has a token must not CREATE that island's directory.
//
// This is #347's defect on a second path. porttoken.Load resolved through
// paths.TokenPath, which MkdirAlls, so a lookup for an island that does not
// exist brought ~/.dejima/projects/<name>/ into being and then answered "no".
//
// Reported as purge leftovers: an operator who had purged several islands found
// a directory per purged name still sitting there, each empty. project.List
// skips a dir with no config.toml, so they were invisible to every surface while
// looking exactly like deletes that had failed — which is what they were
// reported as.
func TestLoadDoesNotCreateTheIslandDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tok, err := Load("purged-island")
	if err != nil {
		t.Fatalf("Load of a missing island should be a quiet miss, got: %v", err)
	}
	if tok != "" {
		t.Errorf("got a token for an island that does not exist: %q", tok)
	}

	dir := filepath.Join(home, ".dejima", "projects", "purged-island")
	if _, err := os.Stat(dir); err == nil {
		t.Error("the read created the island directory it was asked about — after a " +
			"purge this is the leftover that looks like a failed delete")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat: %v", err)
	}
}

// The WRITE path still needs somewhere to land, so minting must create the dir.
// Without this control the fix above could be "make TokenPath never create",
// which would break issuing a token for a real island.
func TestEnsureStillCreatesWhatItNeeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tok, err := Ensure("real-island")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if tok == "" {
		t.Fatal("no token minted")
	}
	got, err := Load("real-island")
	if err != nil || got != tok {
		t.Errorf("Load after Ensure = %q, %v; want the minted token", got, err)
	}
}
