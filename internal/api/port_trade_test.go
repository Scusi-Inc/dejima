package api

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveWriteTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	t.Run("new file in existing dir", func(t *testing.T) {
		if _, _, err := resolveWriteTarget(root, "sub/new.md"); err != nil {
			t.Errorf("unexpected: %v", err)
		}
	})
	t.Run("new file in a new subdir", func(t *testing.T) {
		if _, _, err := resolveWriteTarget(root, "fresh/deep/x.md"); err != nil {
			t.Errorf("unexpected: %v", err)
		}
	})
	t.Run("parent traversal refused", func(t *testing.T) {
		if _, _, err := resolveWriteTarget(root, "../escape.md"); err == nil {
			t.Error("expected traversal refusal")
		}
	})
	t.Run("absolute refused", func(t *testing.T) {
		if _, _, err := resolveWriteTarget(root, "/etc/x"); err == nil {
			t.Error("expected absolute refusal")
		}
	})
	t.Run("write through a symlink refused", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip()
		}
		if err := os.Symlink(filepath.Join(outside, "target.md"), filepath.Join(root, "link.md")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, _, err := resolveWriteTarget(root, "link.md"); err == nil {
			t.Error("expected symlink-target refusal")
		}
	})
	t.Run("symlinked parent dir escaping refused", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip()
		}
		if err := os.Symlink(outside, filepath.Join(root, "evil")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, _, err := resolveWriteTarget(root, "evil/x.md"); err == nil {
			t.Error("expected symlinked-parent escape refusal")
		}
	})
}

func TestResolveWithinScope(t *testing.T) {
	root := t.TempDir()
	// scope/note.md and scope/sub/deep.md
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deep.md"), []byte("deep"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A secret outside the scope, and a symlink inside the scope pointing at it.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("allows files within scope", func(t *testing.T) {
		for _, rel := range []string{"note.md", "sub/deep.md"} {
			if _, _, err := resolveWithinScope(root, rel); err != nil {
				t.Errorf("rel %q: unexpected error %v", rel, err)
			}
		}
	})

	t.Run("rejects parent traversal", func(t *testing.T) {
		if _, _, err := resolveWithinScope(root, "../../etc/hostname"); err == nil {
			t.Error("expected traversal to be rejected")
		}
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		if _, _, err := resolveWithinScope(root, "/etc/passwd"); err == nil {
			t.Error("expected absolute path to be rejected")
		}
	})

	t.Run("rejects symlink escaping scope", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink semantics differ on windows")
		}
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if _, _, err := resolveWithinScope(root, "escape"); err == nil {
			t.Error("expected symlink escape to be rejected")
		}
	})
}
