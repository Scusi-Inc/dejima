// Package vmmem reasons about the container runtime's memory ceiling. On macOS,
// Docker doesn't run containers natively — colima/Docker Desktop runs a Linux VM
// given a FIXED slice of host RAM, and that slice is the ceiling for ALL islands
// combined. A too-small VM (colima defaults to 2 GB) is the substrate-level cause
// of island OOMs (#23): no per-island knob helps when the whole pool is 2 GB on a
// 24 GB host. These helpers read host RAM, recommend a VM size, and judge whether
// the current VM is undersized — shared by the daemon (overview → TUI banner) and
// `dejima doctor` (the host-side check + colima fix).
package vmmem

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const gib = 1 << 30

var (
	hostOnce  sync.Once
	hostBytes uint64
)

// HostMemoryBytes returns the host's physical RAM (cached — it doesn't change
// over a process's life). macOS: `sysctl hw.memsize`. Linux: MemTotal from
// /proc/meminfo. 0 when it can't be determined (callers treat 0 as "unknown",
// never as a problem).
func HostMemoryBytes() uint64 {
	hostOnce.Do(func() { hostBytes = readHostMemory() })
	return hostBytes
}

func readHostMemory() uint64 {
	switch runtime.GOOS {
	case "darwin":
		// ABSOLUTE PATH FIRST. A launchd daemon inherits a bare PATH, and the
		// dejimad plist narrows it further, so `exec.Command("sysctl", …)` can
		// fail to resolve on a Mac that plainly has sysctl. It returns 0, which
		// every surface renders as "host RAM unknown" — an operator picking a
		// local model was shown "needs 8 GiB · host RAM unknown" on every row and
		// had to go and read `sysctl` themselves.
		//
		// Third time this exact fix has been needed in this area: resolveExe for
		// the ollama binary, findBrew for Homebrew, and now this. PATH is not a
		// reliable way for a daemon to find anything.
		for _, exe := range []string{"/usr/sbin/sysctl", "sysctl"} {
			out, err := exec.Command(exe, "-n", "hw.memsize").Output()
			if err != nil {
				continue
			}
			if n, perr := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); perr == nil && n > 0 {
				return n
			}
		}
		return 0
	case "linux":
		b, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(line, "MemTotal:") {
				continue
			}
			f := strings.Fields(line) // "MemTotal:  N kB"
			if len(f) >= 2 {
				kb, _ := strconv.ParseUint(f[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

// RecommendedBytes is the VM size to suggest for a host: three-quarters of host
// RAM, but always leaving the host at least 4 GiB so macOS itself isn't starved
// — min(¾·host, host−4 GiB) — rounded down to a whole GiB, floored at 2 GiB.
// 0 when host is unknown.
func RecommendedBytes(host uint64) uint64 {
	if host == 0 {
		return 0
	}
	rec := host / 4 * 3 // ¾ host
	// Always leave the host at least 4 GiB — min(¾·host, host−4 GiB). For hosts
	// ≤4 GiB the floor is 0, so rec collapses to the 2 GiB minimum below rather
	// than handing nearly all of a tiny host to the VM.
	floor := uint64(0)
	if host > 4*gib {
		floor = host - 4*gib
	}
	if floor < rec {
		rec = floor
	}
	rec = rec / gib * gib // whole GiB
	if rec < 2*gib {
		rec = 2 * gib
	}
	return rec
}

// RecommendedGB is RecommendedBytes in whole GiB (for the colima --memory flag).
func RecommendedGB(host uint64) int { return int(RecommendedBytes(host) / gib) }

// Undersized reports whether the VM is meaningfully smaller than recommended —
// the trigger for the doctor warning + TUI banner. Only fires when there's real
// headroom being wasted (VM below ¾ of the recommendation), so a VM that's merely
// a bit under ideal doesn't nag. False when either figure is unknown.
func Undersized(host, vm uint64) bool {
	if host == 0 || vm == 0 {
		return false
	}
	return vm < RecommendedBytes(host)/4*3
}

// ColimaAvailable reports whether the colima CLI is on PATH (so the fix can be
// scripted). Docker Desktop has no CLI resize — its memory is a GUI slider.
func ColimaAvailable() bool {
	_, err := exec.LookPath("colima")
	return err == nil
}
