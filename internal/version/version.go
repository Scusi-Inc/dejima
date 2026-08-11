package version

import (
	"strconv"
	"strings"
)

// Version is the semver build version, set at build time via -ldflags.
// "dev" (or a git-describe string) for un-tagged local builds.
var Version = "dev"

// APIVersion is the daemon's HTTP API contract version. Bump it on any
// backwards-incompatible change to request/response shapes or routes (additive
// optional fields don't count). The client compares its compiled-in value to
// the daemon's reported value to detect skew instead of silently degrading.
const APIVersion = 1

// IsRelease reports whether v carries a usable semver core (vX.Y.Z), as opposed
// to "dev". Deliberately LENIENT: parseSemver drops any -prerelease/+build
// suffix, so a git-describe string like "v0.8.60-3-gabc1234" also passes. That's
// what comparison callers want — such a build really is "at least v0.8.60" for
// skew checks. Callers that need a version to name a PUBLISHED artifact (a
// release asset to download) must use IsExactRelease instead.
func IsRelease(v string) bool {
	_, ok := parseSemver(v)
	return ok
}

// IsExactRelease reports whether v is a clean release tag — vX.Y.Z with no
// -prerelease/+build suffix — and so names a real published release. Use this
// (not IsRelease) whenever the version has to resolve to a downloadable asset or
// a git tag: "v0.8.60-3-gabc1234" is three commits PAST v0.8.60 and no release
// by that name exists, so fetching it 404s.
func IsExactRelease(v string) bool {
	return IsRelease(v) && !strings.ContainsAny(strings.TrimSpace(v), "-+")
}

// Compare returns -1, 0, or +1 comparing two semver strings (a leading 'v' and
// any -prerelease/+build suffix are ignored). Non-semver inputs ("dev", hashes)
// sort below any real release and compare equal to each other — callers should
// treat an unknown-vs-unknown 0 as "can't tell".
func Compare(a, b string) int {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		switch {
		case oka && !okb:
			return 1
		case !oka && okb:
			return -1
		default:
			return 0
		}
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parseSemver extracts the numeric MAJOR.MINOR.PATCH core of a version string.
func parseSemver(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
