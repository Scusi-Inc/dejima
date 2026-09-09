package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// A TEST BINARY MUST NEVER GET A TERMINAL, however real the one it is running
// on. This is the guard, not a formality.
//
// cmd/dejima/sudo.go records what happens without it: `go test ./...` printed
// "[sudo] password for …" onto the developer's screen mid-suite, read input
// from it, with nothing naming which test was asking — and the suite still
// reported PASS. CI never saw it, because a runner has no controlling terminal.
// It shows up only where a person is watching.
//
// This package grew its own openTTY (it cannot import cmd/dejima), so it needs
// its own copy of that guard and its own proof.
func TestATestBinaryNeverGetsATerminal(t *testing.T) {
	if openTTY() != nil {
		t.Fatal("openTTY handed a test binary a terminal — elevatedInstall will " +
			"prompt for a password on whoever's screen is running the suite, and " +
			"the suite will still pass")
	}
}

// The interactive branch is gated on openTTY and nothing else, so with no
// terminal the caller still gets the advice that names a command they can run.
func TestWithNoTerminalTheAdviceStillNamesACommand(t *testing.T) {
	orig := openTTY
	openTTY = func() *os.File { return nil }
	t.Cleanup(func() { openTTY = orig })

	if adv := ElevationAdvice(); !strings.Contains(adv, "sudo dejima update") {
		t.Errorf("the fallback advice no longer names a runnable command: %q", adv)
	}
}
