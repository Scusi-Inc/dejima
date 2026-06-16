//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// rootOwnedStateFiles returns top-level files in dir owned by a different user
// than the one running — i.e. written by root under sudo, which the daemon
// (running as this user) can't read. Directories are skipped.
func rootOwnedStateFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	uid := os.Getuid()
	var bad []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
			bad = append(bad, filepath.Join(dir, e.Name()))
		}
	}
	return bad
}
