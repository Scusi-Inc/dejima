package vmmem

import "testing"

// The case that actually happened, pinned.
//
// A 24 GB / 10-core Mac mini, nine islands, ~20 agents, and `docker info`
// reporting `2 cpus`. Memory was exactly the recommended 18 GiB — which is why
// nothing caught it: the memory check passed, and passing was the thing that
// hid it.
func TestTheTwoCPUDefaultOnATenCoreHostIsFlagged(t *testing.T) {
	if !CPUUndersized(10, 2) {
		t.Fatal("a 2-CPU VM on a 10-core host reads as fine — this is the exact " +
			"configuration that made nine islands crawl for a day while every " +
			"check reported OK")
	}
	if got := RecommendedCPU(10); got != 8 {
		t.Errorf("RecommendedCPU(10) = %d, want 8 (all but two cores)", got)
	}
}

// The control: a correctly-sized VM must NOT warn.
//
// Without this, a check that returned true unconditionally would pass the test
// above and nag every operator on every run until someone deleted it.
func TestACorrectlySizedVMIsQuiet(t *testing.T) {
	for _, tc := range []struct{ host, vm int }{
		{10, 8}, // the recommendation itself
		{10, 6}, // ¾ of it — deliberately conservative, still fine
		{8, 6},
		{4, 2}, // small host: recommendation floors at 2, and 2 is what it has
		{2, 2},
	} {
		if CPUUndersized(tc.host, tc.vm) {
			t.Errorf("host=%d vm=%d warns, but that is a reasonable VM — a check "+
				"that nags on correct configurations gets switched off",
				tc.host, tc.vm)
		}
	}
}

// Unknown is not a finding.
//
// Mirrors Undersized: HostMemoryBytes returns 0 when it cannot read the host,
// and every surface is required to render that as "not determined" rather than
// as a problem. A zero here must not manufacture a warning about a VM nobody
// managed to measure.
func TestUnknownCPUCountsNeverWarn(t *testing.T) {
	for _, tc := range []struct{ host, vm int }{
		{0, 2}, {10, 0}, {0, 0}, {-1, 4}, {10, -1},
	} {
		if CPUUndersized(tc.host, tc.vm) {
			t.Errorf("host=%d vm=%d warned on an unknown figure", tc.host, tc.vm)
		}
	}
	if got := RecommendedCPU(0); got != 0 {
		t.Errorf("RecommendedCPU(0) = %d, want 0 — an unknown host yields no "+
			"recommendation to put in a repair command", got)
	}
}

// The floor exists so a small host still gets a usable VM.
func TestRecommendedCPUFloorsAtTwo(t *testing.T) {
	for _, host := range []int{1, 2, 3, 4} {
		if got := RecommendedCPU(host); got < 2 {
			t.Errorf("RecommendedCPU(%d) = %d — a VM with fewer than 2 cores is not "+
				"worth recommending to anyone", host, got)
		}
	}
}

// The two ceilings must nag on the same terms.
//
// Memory warns below ¾ of its recommendation and says so in a comment about not
// nagging a VM that is merely a bit under ideal. CPU inheriting a different
// threshold would make one of the two rows feel arbitrary, and the arbitrary one
// is the one that gets ignored.
func TestCPUAndMemoryUseTheSameThreeQuartersRule(t *testing.T) {
	// 10 cores → recommend 8 → ¾ is 6, so 5 warns and 6 does not.
	if CPUUndersized(10, 6) {
		t.Error("6 of a recommended 8 warns — stricter than the memory rule")
	}
	if !CPUUndersized(10, 5) {
		t.Error("5 of a recommended 8 does not warn — looser than the memory rule")
	}
}
