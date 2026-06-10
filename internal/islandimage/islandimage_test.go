package islandimage

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterializeMatchesSourceTree walks the real image/ directory and checks
// the materialized context reproduces it byte-for-byte — i.e. a daemon-side
// `docker build` sees exactly what `make image` sees.
func TestMaterializeMatchesSourceTree(t *testing.T) {
	srcRoot := filepath.Join("..", "..", "image")
	if _, err := os.Stat(srcRoot); err != nil {
		t.Skipf("source image/ dir not available: %v", err)
	}

	dir, cleanup, err := Materialize()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	count := 0
	err = filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		materialized := filepath.Join(dir, "image", rel)
		got, err := os.ReadFile(materialized)
		if err != nil {
			t.Errorf("missing from materialized context: image/%s (%v)", rel, err)
			return nil
		}
		if !bytes.Equal(got, want) {
			t.Errorf("content mismatch for image/%s", rel)
		}
		if strings.HasSuffix(rel, ".sh") {
			info, err := os.Stat(materialized)
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o100 == 0 {
				t.Errorf("image/%s lost its exec bit", rel)
			}
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("source tree walk found no files")
	}

	if _, err := os.Stat(filepath.Join(dir, Dockerfile)); err != nil {
		t.Fatalf("Dockerfile not at expected path %s: %v", Dockerfile, err)
	}
}
