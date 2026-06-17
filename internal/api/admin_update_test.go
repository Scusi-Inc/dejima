package api

import (
	"runtime"
	"testing"

	"github.com/aoos/dejima/internal/selfupdate"
)

// TestPreflightPrivileged covers the no-op paths that don't depend on the host's
// /etc/sudoers.d/dejima: a non-system install never needs privilege, and only
// macOS system installs gate on the drop-in. The darwin+system case touches a
// real root-owned path, so it's left to live verification.
func TestPreflightPrivileged(t *testing.T) {
	if err := preflightPrivileged(selfupdate.InstallMeta{System: false}); err != nil {
		t.Errorf("non-system install should never need privilege preflight, got %v", err)
	}
	if runtime.GOOS != "darwin" {
		if err := preflightPrivileged(selfupdate.InstallMeta{System: true}); err != nil {
			t.Errorf("non-darwin system install has no sudoers preflight, got %v", err)
		}
	}
}
