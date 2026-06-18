package vmmem

import "testing"

func TestRecommendedBytes(t *testing.T) {
	cases := []struct {
		hostGB int
		wantGB int
	}{
		{24, 18}, // ¾·24 = 18, and 24−4 = 20 → min 18
		{16, 12}, // ¾·16 = 12, and 16−4 = 12 → 12
		{8, 4},   // ¾·8 = 6, but 8−4 = 4 → min 4 (host keeps 4 GiB)
		{4, 2},   // host−4 = 0 → floored to the 2 GiB minimum
		{0, 0},   // unknown host → unknown
	}
	for _, c := range cases {
		got := RecommendedBytes(uint64(c.hostGB) * gib)
		if got != uint64(c.wantGB)*gib {
			t.Errorf("RecommendedBytes(%dGB) = %d GiB, want %d", c.hostGB, got/gib, c.wantGB)
		}
	}
}

func TestUndersized(t *testing.T) {
	const host = 24 * gib // recommends 18 GiB; warns below ¾·18 = 13.5
	cases := []struct {
		vmGB float64
		want bool
	}{
		{2, true},   // colima default — way under
		{8, true},   // still under 13.5
		{16, false}, // comfortably above the warn threshold
		{18, false}, // at recommendation
	}
	for _, c := range cases {
		if got := Undersized(host, uint64(c.vmGB*gib)); got != c.want {
			t.Errorf("Undersized(24GB host, %.0fGB vm) = %v, want %v", c.vmGB, got, c.want)
		}
	}
	// Unknown figures never warn (never cry wolf without data).
	if Undersized(0, 2*gib) || Undersized(host, 0) {
		t.Error("Undersized must be false when host or vm is unknown (0)")
	}
}
