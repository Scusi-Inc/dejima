package fdlimit

import (
	"syscall"
	"testing"
)

// TestRaiseGivesHeadroom is the regression test for the launchd-256 stall: a
// daemon starting with a low soft limit must come out of Raise with enough
// descriptors to serve concurrent egress tunnels. Simulates the launchd
// environment by dropping the soft limit to 256 first.
func TestRaiseGivesHeadroom(t *testing.T) {
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &orig); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &orig) })

	low := orig
	low.Cur = 256
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &low); err != nil {
		t.Skipf("cannot lower RLIMIT_NOFILE in this environment: %v", err)
	}

	res, err := Raise()
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if res.Was != 256 {
		t.Errorf("Was = %d, want 256", res.Was)
	}
	if !res.Raised {
		t.Error("Raised = false, want true — 256 is below Target")
	}
	// The exact ceiling is kernel-dependent; what matters is real headroom
	// beyond the couple hundred descriptors that were causing the stall.
	if res.Now < 4096 {
		t.Errorf("soft limit after Raise = %d, want at least 4096", res.Now)
	}

	var now syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &now); err != nil {
		t.Fatalf("getrlimit after: %v", err)
	}
	if uint64(now.Cur) != res.Now {
		t.Errorf("reported %d but kernel says %d", res.Now, now.Cur)
	}
}

// TestRaiseIsIdempotent: a second call when the limit is already at or above
// Target must report no change rather than churning the limit.
func TestRaiseIsIdempotent(t *testing.T) {
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &orig); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &orig) })

	first, err := Raise()
	if err != nil {
		t.Skipf("Raise unavailable here: %v", err)
	}
	second, err := Raise()
	if err != nil {
		t.Fatalf("second Raise: %v", err)
	}
	if second.Raised {
		t.Error("second Raise reported a change; want no-op")
	}
	if second.Now != first.Now {
		t.Errorf("limit moved between calls: %d then %d", first.Now, second.Now)
	}
}
