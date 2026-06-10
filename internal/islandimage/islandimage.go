// Package islandimage materializes the embedded island-image build context so
// the daemon can run `docker build` without a source checkout.
package islandimage

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	dejima "github.com/aoos/dejima"
)

// Dockerfile is the Dockerfile's path relative to the materialized context
// dir, matching the source tree layout (and the Makefile's -f argument).
const Dockerfile = "image/Dockerfile"

// Materialize writes the embedded build context into a fresh temp dir laid
// out exactly like the source tree (image/Dockerfile, image/start.sh, …), so
// `docker build -f image/Dockerfile <dir>` reproduces `make image`. The
// caller must invoke cleanup to remove the dir.
func Materialize() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "dejima-island-image-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	err = fs.WalkDir(dejima.IslandImageFS, "image", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := dejima.IslandImageFS.ReadFile(path)
		if err != nil {
			return err
		}
		// go:embed drops permission bits; restore exec on shell scripts so
		// COPY'd shims keep their source-tree mode inside the image.
		mode := os.FileMode(0o644)
		if strings.HasSuffix(path, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}
