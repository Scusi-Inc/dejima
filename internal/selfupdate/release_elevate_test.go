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

// A Mac client reported:
//
//	⚠ client update failed: replace /usr/local/bin/dejima:
//	  rename /var/folders/b5/515sk85x0ys14sjl6ltjzmpc00…
//
// dirWritable had already found /usr/local/bin unwritable and staged into
// $TMPDIR — and on macOS /var/folders need not share a filesystem with
// /usr/local/bin, so the rename came back EXDEV. That is not a permission error,
// so the elevation path was skipped and the update refused itself over where it
// had put its own temp file.
//
// The bug was gating the remedy on WHY the rename failed. Once we have staged
// outside, we already know it cannot work unaided.
func TestElevationIsNotGatedOnTheRenameErrno(t *testing.T) {
	exdev := &os.LinkError{
		Op:  "rename",
		Old: "/var/folders/b5/515sk85x0ys14sjl6ltjzmpc0000gn/T/dejima-update-1/dejima",
		New: "/usr/local/bin/dejima",
		Err: syscall.EXDEV,
	}
	if isPermission(exdev) {
		t.Fatal("EXDEV now reads as a permission error, so this test no longer " +
			"reproduces the reported failure and proves nothing")
	}
	if !shouldElevate(true, exdev) {
		t.Error("a cross-device rename out of a temp dir did not reach the elevated " +
			"install — this is the reported Mac failure, unfixed")
	}

	// Staged outside means the dir already refused us: elevate whatever came back.
	for _, err := range []error{exdev, syscall.EACCES, errors.New("boom"), os.ErrNotExist} {
		if !shouldElevate(true, err) {
			t.Errorf("shouldElevate(staged outside, %v) = false — the install dir was "+
				"already proven unwritable, so no errno makes the plain rename viable", err)
		}
	}

	// Staged BESIDE the target: a denial is still worth elevating, but anything
	// else is a different problem and sudo would only hide it.
	if !shouldElevate(false, syscall.EACCES) {
		t.Error("a writable dir that still denied the rename should try elevation")
	}
	if shouldElevate(false, errors.New("disk full")) {
		t.Error("a non-permission failure in a WRITABLE dir was sent to sudo, which " +
			"cannot fix it and buries the real cause")
	}
}
