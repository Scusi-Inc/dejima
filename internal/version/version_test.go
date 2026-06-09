package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.1.0", 0},
		{"v0.1.0", "v0.2.0", -1},
		{"v0.2.0", "v0.1.0", 1},
		{"1.2.3", "v1.2.3", 0},      // 'v' optional
		{"v1.0.0", "v0.9.9", 1},     // major dominates
		{"v0.1.0-rc1", "v0.1.0", 0}, // prerelease suffix ignored
		{"dev", "dev", 0},           // unknown == unknown
		{"v0.1.0", "dev", 1},        // release beats unknown
		{"dev", "v0.1.0", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsRelease(t *testing.T) {
	for _, v := range []string{"v0.1.0", "1.2.3", "v10.20.30"} {
		if !IsRelease(v) {
			t.Errorf("IsRelease(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"dev", "9094c9f-dirty", "v1.2", ""} {
		if IsRelease(v) {
			t.Errorf("IsRelease(%q) = true, want false", v)
		}
	}
}
