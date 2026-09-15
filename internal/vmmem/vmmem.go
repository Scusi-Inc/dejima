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
	"fmt"
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

// The VM has a CPU ceiling too, and nothing has ever looked at it.
//
// Same substrate shape as the memory ceiling and a worse failure, because it is
// silent in both directions. colima defaults to 2 CPUs; `colima start --memory N`
// sets memory and LEAVES CPU AT THE DEFAULT. So the ordinary way an operator
// fixes an OOM — size the memory up, which the doctor tells them to do — produces
// a VM that is memory-correct and CPU-starved, and every check then passes.
//
// Observed 2026-09-14 on a 24 GB / 10-core Mac mini running nine islands and
// about twenty agents: `docker info` reported `2 cpus / 18818494464 bytes`.
// Memory was exactly the recommended 18 GiB. Two cores served the lot. Clone,
// agent start and first paint all crawled, `dejima doctor` reported vm memory OK,
// and the operator spent a day believing the Mac mini was too small.

// RecommendedCPU is the VM CPU count to suggest for a host: all but two cores,
// leaving the host itself something to run on, floored at 2. 0 when unknown.
//
// The same arithmetic the colima repair in `dejima doctor` already used when it
// resized for MEMORY — lifted here so it is a shared rule with a name rather than
// a number that happened to live inside one fix.
func RecommendedCPU(hostCPU int) int {
	if hostCPU <= 0 {
		return 0
	}
	rec := hostCPU - 2
	if rec < 2 {
		rec = 2
	}
	return rec
}

// CPUUndersized reports whether the VM has meaningfully fewer cores than
// recommended — below ¾, mirroring Undersized so the two ceilings nag on the
// same terms and a deliberate, slightly-conservative VM does not.
//
// False when either figure is unknown: an unasked question is not a finding.
func CPUUndersized(hostCPU, vmCPU int) bool {
	if hostCPU <= 0 || vmCPU <= 0 {
		return false
	}
	return vmCPU < RecommendedCPU(hostCPU)*3/4
}

// ResizeTo computes the sizing a `colima start` should apply to BOTH dimensions:
// the RECOMMENDATION for a dimension that is undersized, and the VM's CURRENT
// value for one that is not.
//
// Both flags, always. Omitting one is the whole bug: `colima start --memory N`
// applies the memory and leaves CPU at colima's 2-core default, so an operator
// who fixes an OOM the way we told them to ends up memory-correct and
// CPU-starved with every check green. That is not a hypothetical — it read as
// "the Mac mini is too small" for a day on a 10-core host running nine islands.
//
// And the value for the HEALTHY dimension is the current one, not the
// recommendation, because a passing check is not an invitation to resize. The
// ceilings nag below ¾ of recommended, so a deliberately conservative VM passes
// on purpose; an operator who chose 8 cores on a 12-core host gets to keep 8.
// The two doctor checks used to disagree about this — the CPU repair carried the
// VM's real memory through while the memory repair quietly reset CPU to the
// recommendation — and disagreeing was the defect, whichever answer won.
//
// A current value of zero means "no VM yet, or unreadable", and yields the
// recommendation: at install time there is no operator choice to preserve.
func ResizeTo(hostCPU int, hostBytes uint64, vmCPU int, vmBytes uint64) (cpu, memGB int) {
	cpu = vmCPU
	if cpu <= 0 || CPUUndersized(hostCPU, vmCPU) {
		cpu = RecommendedCPU(hostCPU)
	}
	memGB = int(vmBytes / (1 << 30))
	if memGB <= 0 || Undersized(hostBytes, vmBytes) {
		memGB = RecommendedGB(hostBytes)
	}
	return cpu, memGB
}

// ColimaStartArgs is the argv for a `colima start` that sizes both dimensions.
// Shared so the command we PRINT and the command we RUN cannot drift apart.
func ColimaStartArgs(cpu, memGB int) []string {
	return []string{"start", "--cpu", strconv.Itoa(cpu), "--memory", strconv.Itoa(memGB)}
}

// ColimaResizeCmd is the operator-facing one-liner for an EXISTING VM. colima
// cannot resize a running VM, hence the stop.
func ColimaResizeCmd(cpu, memGB int) string {
	return fmt.Sprintf("colima stop && colima start --cpu %d --memory %d", cpu, memGB)
}

// ColimaStartCmd is the same sizing for a VM that does not exist yet, where
// there is nothing to stop first.
func ColimaStartCmd(cpu, memGB int) string {
	return fmt.Sprintf("colima start --cpu %d --memory %d", cpu, memGB)
}
