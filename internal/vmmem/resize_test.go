package vmmem

import (
	"strings"
	"testing"
)

// The two doctor repairs used to disagree about the dimension they were NOT
// fixing: the CPU repair carried the VM's real memory through, while the memory
// repair quietly reset CPU to `runtime.NumCPU() - 2`. Both emitted a single
// command with both flags, so the disagreement was invisible in the output —
// what differed was which of the operator's choices survived it.
//
// ResizeTo is that decision in one place: recommend the undersized dimension,
// keep the healthy one.
func TestResizeToKeepsTheDimensionThatPassed(t *testing.T) {
	const hostCPU = 12
	const hostRAM = uint64(24) * gib

	// The live case on 2026-09-15: the operator was told "recommend 10" and chose
	// 8. 8 is above the ¾ floor, so the CPU check passes — and a later memory fix
	// must not quietly move it to 10 on their behalf.
	t.Run("a deliberately conservative CPU count survives a memory fix", func(t *testing.T) {
		cpu, memGB := ResizeTo(hostCPU, hostRAM, 8, uint64(6)*gib)
		if cpu != 8 {
			t.Errorf("cpu = %d, want 8 — the operator chose 8, it passes its own check, "+
				"and a memory repair is not the place to overrule that", cpu)
		}
		if want := RecommendedGB(hostRAM); memGB != want {
			t.Errorf("memGB = %d, want %d — memory is the undersized dimension here", memGB, want)
		}
	})

	t.Run("a deliberately conservative memory size survives a CPU fix", func(t *testing.T) {
		cpu, memGB := ResizeTo(hostCPU, hostRAM, 2, uint64(17)*gib)
		if want := RecommendedCPU(hostCPU); cpu != want {
			t.Errorf("cpu = %d, want %d — CPU is the undersized dimension here", cpu, want)
		}
		if memGB != 17 {
			t.Errorf("memGB = %d, want 17 — resetting a 17GB VM while fixing cores "+
				"would be a worse bug than the one being fixed", memGB)
		}
	})

	t.Run("both undersized gets both recommendations", func(t *testing.T) {
		cpu, memGB := ResizeTo(hostCPU, hostRAM, 2, uint64(4)*gib)
		if cpu != RecommendedCPU(hostCPU) || memGB != RecommendedGB(hostRAM) {
			t.Errorf("got %d CPU / %dGB, want the full recommendation %d / %d",
				cpu, memGB, RecommendedCPU(hostCPU), RecommendedGB(hostRAM))
		}
	})

	// Install time: no VM exists, so there is no operator choice to preserve and
	// the recommendation is the only sensible answer. This is the case provision
	// and onboard hit, and the one that used to emit --memory with no --cpu.
	t.Run("no VM yet takes the recommendation for both", func(t *testing.T) {
		cpu, memGB := ResizeTo(hostCPU, hostRAM, 0, 0)
		if cpu != RecommendedCPU(hostCPU) || memGB != RecommendedGB(hostRAM) {
			t.Errorf("got %d CPU / %dGB, want the full recommendation %d / %d",
				cpu, memGB, RecommendedCPU(hostCPU), RecommendedGB(hostRAM))
		}
	})
}

// The bug is an ABSENT flag, not a wrong number: `colima start --memory N`
// applies the memory and leaves CPU at colima's 2-core default. Every renderer
// must carry both, so no caller can reintroduce the half-command by hand.
func TestColimaCommandsAlwaysCarryBothFlags(t *testing.T) {
	for name, got := range map[string]string{
		"resize": ColimaResizeCmd(10, 17),
		"start":  ColimaStartCmd(10, 17),
	} {
		if !strings.Contains(got, "--cpu 10") || !strings.Contains(got, "--memory 17") {
			t.Errorf("%s = %q — a sizing command missing either flag leaves that "+
				"dimension at colima's default, which is the whole defect", name, got)
		}
	}

	// A resize needs the stop; a first start has nothing to stop.
	if got := ColimaResizeCmd(10, 17); !strings.Contains(got, "colima stop &&") {
		t.Errorf("ColimaResizeCmd = %q — colima cannot resize a running VM", got)
	}
	if got := ColimaStartCmd(10, 17); strings.Contains(got, "colima stop") {
		t.Errorf("ColimaStartCmd = %q — there is no VM to stop at install time", got)
	}

	// The printed command and the executed argv come from the same place on
	// purpose; pin that they agree, since drift between them is silent.
	args := ColimaStartArgs(10, 17)
	joined := "colima"
	for _, a := range args {
		joined += " " + a
	}
	if joined != ColimaStartCmd(10, 17) {
		t.Errorf("argv %q and printed %q disagree — the command we run must be the "+
			"command we showed", joined, ColimaStartCmd(10, 17))
	}
}
