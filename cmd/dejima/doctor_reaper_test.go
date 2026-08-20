package main

import (
	"strings"
	"testing"
)

// An island's container either has an init as PID 1 or it doesn't, and that is
// fixed at create time. The daemon passes --init now, which means the daemon's
// own source says every island reaps — and every container made before that flag
// says otherwise, for the rest of its life. Only the runtime can tell them apart,
// so the point of these tests is that the three answers stay three answers.

func boolPtr(b bool) *bool { return &b }

func TestReaperDiagnosisWarnsWhenNothingReaps(t *testing.T) {
	f := diagnoseOrphanReaping(boolPtr(false), "alpha")
	if f.status != "WARN" {
		t.Fatalf("status = %q, want WARN", f.status)
	}
	if !strings.Contains(f.fix, "dejima upgrade alpha") {
		t.Errorf("the fix must name the recreate, since it cannot be fixed in place: %q", f.fix)
	}
	if !strings.Contains(f.detail, "zombie") {
		t.Errorf("the detail should name the symptom an operator can see in `ps`: %q", f.detail)
	}
}

func TestReaperDiagnosisSaysNothingWhenItReaps(t *testing.T) {
	if f := diagnoseOrphanReaping(boolPtr(true), "alpha"); f.status != "" {
		t.Errorf("a reaping container is not a finding: %+v", f)
	}
}

// The case this whole three-state shape exists for. "I couldn't ask the runtime"
// is not "the container is fine", and rendering it as silence would put a clean
// row next to an island nobody checked.
func TestReaperDiagnosisReportsNotKnowingAsNotKnowing(t *testing.T) {
	f := diagnoseOrphanReaping(nil, "alpha")
	if f.status == "" {
		t.Fatal("an undetermined answer must produce a row; silence reads as 'checked and fine'")
	}
	if f.status == "WARN" || f.status == "FAIL" {
		t.Errorf("not knowing is not a verdict — status %q accuses a container we never inspected", f.status)
	}
	if f.fix != "" {
		t.Errorf("no remedy can be offered for a state we didn't determine, got %q", f.fix)
	}
}

// The control. The three inputs must produce three DIFFERENT outcomes — if any
// two collapse, the tests above still pass individually while the check has
// stopped distinguishing the thing it exists to distinguish.
func TestReaperDiagnosisKeepsTheThreeStatesDistinct(t *testing.T) {
	seen := map[string]string{}
	for label, in := range map[string]*bool{
		"reaps":     boolPtr(true),
		"does not":  boolPtr(false),
		"not known": nil,
	} {
		st := diagnoseOrphanReaping(in, "alpha").status
		if prev, dup := seen[st]; dup {
			t.Errorf("%q and %q both produce status %q — the check no longer separates them",
				prev, label, st)
		}
		seen[st] = label
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct outcomes, got %d: %v", len(seen), seen)
	}
}
