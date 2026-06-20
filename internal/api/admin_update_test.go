package api

import (
	"io"
	"log/slog"
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

// TestAttachedSessions verifies the count that gates a daemon self-update
// restart: it must aggregate attached clients across every island and agent
// tracker, and drop back to zero as they detach. A self-update only proceeds
// (without Force) when this is zero, so an off-by-one here would either yank
// live terminals or wedge updates forever.
func TestAttachedSessions(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if got := len(s.attachedSessions()); got != 0 {
		t.Fatalf("fresh server: want 0 attached, got %d", got)
	}

	// Two clients on one island's primary agent, one on another island.
	h1, _ := s.trackerFor("alpha", "a1").Attach("laptop", func() {})
	_, _ = s.trackerFor("alpha", "a1").Attach("phone", func() {})
	h3, _ := s.trackerFor("beta", "a1").Attach("desktop", func() {})

	if got := len(s.attachedSessions()); got != 3 {
		t.Fatalf("after 3 attaches: want 3, got %d", got)
	}

	// Detaching reduces the count; the gate must reopen once everyone leaves.
	s.trackerFor("alpha", "a1").Detach(h1)
	s.trackerFor("beta", "a1").Detach(h3)
	if got := len(s.attachedSessions()); got != 1 {
		t.Fatalf("after 2 detaches: want 1, got %d", got)
	}
}
