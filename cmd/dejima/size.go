package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parseSize turns "512MiB", "2G", "1.5gb" or a bare byte count into bytes.
//
// It RETURNS AN ERROR on anything it does not understand, which is the whole
// reason it exists rather than reusing internal/runtime's parseBytes: that one
// swallows the error from strconv and returns 0 for junk. Zero is a meaningful
// value here — it means "use the server default" — so a typo would not raise the
// cap, it would silently reset it to the default and refuse the import again
// with the same numbers. A cap-raising flag that quietly does nothing is worse
// than no flag, because the operator concludes the cap cannot be raised.
func parseSize(s string) (int64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("empty size")
	}
	// Longest suffix first: "GiB" must win over "B", and "GB" over "B".
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"KB", 1000}, {"MB", 1000_000}, {"GB", 1000_000_000}, {"TB", 1000_000_000_000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
		{"B", 1},
	}
	up := strings.ToUpper(t)
	num, mult := up, int64(1)
	for _, u := range units {
		if len(up) > len(u.suffix) && strings.HasSuffix(up, u.suffix) {
			num, mult = strings.TrimSpace(up[:len(up)-len(u.suffix)]), u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("not a size: %q (try 512MiB, 2G, or a byte count)", s)
	}
	if v < 0 {
		return 0, fmt.Errorf("size cannot be negative: %q", s)
	}
	// Overflow would wrap to a negative cap, which the daemon reads as "unset"
	// and replaces with the default — the same silent-no-op this function exists
	// to prevent, arriving by a different road.
	const maxInt64 = float64(1 << 62)
	if v*float64(mult) >= maxInt64 {
		return 0, fmt.Errorf("size too large: %q", s)
	}
	return int64(v * float64(mult)), nil
}
