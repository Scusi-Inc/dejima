package api

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Substrate storage: how much the islands occupy, and how much room is left
// beside them. Both halves of that question had an answer in here already and
// neither was reachable from the dashboard: per-island volume sizes on the
// detail endpoint, a host-wide sum on /v1/aggregate behind a panel you have to
// know to open. This file is the shared, attributable version the header reads.

// dejimaVolumePrefix is what project.Project puts on both of an island's volume
// names (`dejima-<name>-workspace`, `dejima-<name>-home`).
const dejimaVolumePrefix = "dejima-"

// dejimaVolumeTotal sums DEJIMA's volumes, and only those.
//
// `docker system df -v` reports every volume on the engine, and the host-wide
// rollup used to sum all of them — so an operator running anything else in
// Docker (a Postgres volume, an unrelated compose stack) had that disk counted
// as Dejima's, in a figure whose own doc comment says "across ALL islands".
//
// The prefix is the attribution, chosen over looking up each live project's two
// volumes: it also keeps volumes LEAKED by a half-finished purge in the total,
// which per-project lookup would silently drop. Those bytes are still ours and
// still occupied, and a number that quietly omits them is the one an operator
// would be holding while wondering where their disk went.
func dejimaVolumeTotal(sizes map[string]int64) int64 {
	var total int64
	for name, sz := range sizes {
		if sz > 0 && strings.HasPrefix(name, dejimaVolumePrefix) {
			total += sz
		}
	}
	return total
}

// diskSpace is the capacity and free space of the filesystem the island volumes
// live on. Zero in either field means undeterminable.
type diskSpace struct {
	CapacityBytes int64
	FreeBytes     int64
}

// engineSpace reports capacity and free space for the filesystem backing the
// island volumes, measured with one `df` FROM INSIDE a running island.
//
// It has to be measured from in there, and this is the part that looks like an
// unnecessary detour until it isn't. On macOS the engine's storage is not the
// daemon's: colima and Docker Desktop keep the volumes in a VM disk image whose
// ceiling is `colima --disk` (60 GB by default), while the daemon process sees
// the Mac's SSD. A statfs on the host would therefore return a number that is
// plausible, much larger, and wrong — and wrong in the generous direction,
// precisely when the operator is asking whether there is room for another
// island. An island's /workspace IS the volume, so df in there reads the pool
// all islands actually share, on colima and on native Linux alike.
//
// The cost of that correctness is a dependency on something running: with no
// island up there is nothing to exec into, and the caller gets the last good
// sample or zero. Callers must treat zero as "unknown" and show used-only,
// never as "no free space".
//
// Cached for 30s on the same reasoning as volumeSizes — an exec per overview
// poll would be one container round-trip every two seconds for a figure that
// moves slowly.
func (s *Server) engineSpace(ctx context.Context, container string) diskSpace {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	if !s.spaceAt.IsZero() && time.Since(s.spaceAt) < 30*time.Second {
		return s.spaceData
	}
	if container == "" {
		return s.spaceData
	}
	stdout, _, code, err := s.rt.Exec(ctx, container, []string{"df", "-kP", "/workspace"})
	if err != nil || code != 0 {
		return s.spaceData
	}
	got := parseDFPosix(stdout)
	if got.CapacityBytes <= 0 {
		return s.spaceData
	}
	s.spaceData, s.spaceAt = got, time.Now()
	return got
}

// parseDFPosix reads the last row of `df -kP` output: 1K blocks in field 1,
// available in field 3.
//
// `-P` is load-bearing. Without it df wraps a long device name onto its own
// line, which shifts every field left by one and turns "available" into the use
// percentage. Measured: that shape parses as 12.6 GB capacity and 0 free — a
// confident wrong number, in the direction that reads as "the disk is full".
//
// Two checks reject it, and they are deliberately redundant because df's output
// is a foreign format (GNU, busybox and macOS df all differ): the six-field
// floor, which a wrapped five-field row fails, and the integer parse, which the
// trailing "%" of the use column fails. Either alone catches the wrapped form
// observed here, so negative-controlling this needs BOTH removed — with one
// gone the other still holds, which is the point of keeping them.
//
// Anything unparseable returns zero, which every caller renders as "unknown".
func parseDFPosix(out string) diskSpace {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	f := strings.Fields(strings.TrimSpace(lines[len(lines)-1]))
	if len(f) < 6 {
		return diskSpace{}
	}
	blocks, errB := strconv.ParseInt(f[1], 10, 64)
	avail, errA := strconv.ParseInt(f[3], 10, 64)
	if errB != nil || errA != nil || blocks <= 0 || avail < 0 {
		return diskSpace{}
	}
	return diskSpace{CapacityBytes: blocks * 1024, FreeBytes: avail * 1024}
}
