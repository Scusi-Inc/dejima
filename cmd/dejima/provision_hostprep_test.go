package main

import (
	"strings"
	"testing"
)

// FileVault and auto-login are mutually exclusive on macOS: the "Automatically
// log in as" control is greyed out while FileVault is on. The un-branched step
// sent the operator to that panel anyway and then looped "auto-login still reads
// as off" — which is what a new operator actually hit, on a Mac mini, with no
// way to satisfy the step and no explanation of why.
func TestAutoLoginStepUnderFileVaultOffersTheRealChoice(t *testing.T) {
	g := autoLoginStep(true)

	// A step nobody can complete must not be pollable, or guide() walks a loop
	// that can never end. It belongs on the end-of-run list instead.
	if g.verify != nil {
		t.Error("the FileVault step is a decision, not a state to poll — it must carry no verify")
	}
	// Both ways out have to be on the page: the one that makes the host
	// self-recovering, and the one that keeps the disk encrypted.
	if !strings.Contains(g.detail, "fdesetup disable") {
		t.Errorf("no route to unattended recovery offered:\n%s", g.detail)
	}
	if !strings.Contains(g.detail, "fdesetup authrestart") {
		t.Errorf("keeping FileVault is a valid choice; the planned-reboot path must be named:\n%s", g.detail)
	}
	// The deeper failure is the one an operator will not guess: FileVault stops
	// the machine at the unlock screen BEFORE any daemon runs, so auto-login was
	// never the whole story.
	if !strings.Contains(g.detail, "unlock screen") {
		t.Errorf("the boot-time consequence is unstated:\n%s", g.detail)
	}
}

// Without FileVault the step is checkable, so it should be walked and confirmed
// rather than listed — that is the whole point of a guided step.
func TestAutoLoginStepWithoutFileVaultIsVerifiable(t *testing.T) {
	g := autoLoginStep(false)
	if g.verify == nil {
		t.Fatal("auto-login is detectable when FileVault is off — it must be verified, not just listed")
	}
	if g.done == "" || g.notYet == "" {
		t.Error("a verifiable step needs both outcomes phrased")
	}
}

// "Right-size the Docker VM" names the action and withholds the one thing the
// operator needs: the number. The wizard has already computed it.
func TestVMRightsizeStepStatesTheTargetSize(t *testing.T) {
	title, detail := vmRightsizeStep(12)

	if !strings.Contains(title, "12GB") {
		t.Errorf("the title must carry the target size, since the checklist prints titles first: %q", title)
	}
	// Both routes to applying it, because which one is right depends on whether
	// they run Docker Desktop or colima — and the GUI path is the one with no
	// number of its own.
	if !strings.Contains(detail, "Memory → 12GB") {
		t.Errorf("the Docker Desktop click path must end at the number:\n%s", detail)
	}
	if !strings.Contains(detail, "--memory 12") {
		t.Errorf("the colima command must be applyable as printed:\n%s", detail)
	}
}

// End to end through the renderer: a size step that reaches the operator without
// a number in it is the bug, wherever it was recorded.
func TestProvManualRightsizeLineCarriesTheNumber(t *testing.T) {
	pc := &provCtx{}
	title, detail := vmRightsizeStep(18)
	pc.addManualFor(whyHost, title, detail)
	out := renderProvManual(pc)

	if !strings.Contains(out, "18GB") {
		t.Errorf("rendered checklist never states the target size:\n%s", out)
	}
}
