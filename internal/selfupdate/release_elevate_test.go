package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The standard install puts the daemon in a root-owned /usr/local/bin while the
// daemon runs as the operator, so it can't even CREATE the staging file there.
// That surfaced as "open /usr/local/bin/.dejimad.update: permission denied" and
// made self-update impossible for the normal layout — the writability probe is
// what routes around it.
func TestDirWritable(t *testing.T) {
	if dirWritable(t.TempDir()) != true {
		t.Error("a temp dir should be writable")
	}
	if dirWritable(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("a missing dir must not report writable")
	}
	// A dir we own but have stripped of write permission stands in for
	// root-owned /usr/local/bin. Skipped as root, which can write regardless.
	if os.Geteuid() == 0 {
		return
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	if dirWritable(locked) {
		t.Error("a non-writable dir reported writable — staging would fail there")
	}
}

// The permission check decides whether to attempt the elevated path at all, so
// it has to recognise the shapes os returns for a denied write.
func TestIsPermission(t *testing.T) {
	for _, err := range []error{
		os.ErrPermission,
		syscall.EACCES,
		syscall.EPERM,
		&os.PathError{Op: "open", Path: "/usr/local/bin/.dejimad.update", Err: syscall.EACCES},
	} {
		if !isPermission(err) {
			t.Errorf("isPermission(%v) = false, want true", err)
		}
	}
	for _, err := range []error{nil, errors.New("boom"), os.ErrNotExist} {
		if isPermission(err) {
			t.Errorf("isPermission(%v) = true, want false", err)
		}
	}
}
