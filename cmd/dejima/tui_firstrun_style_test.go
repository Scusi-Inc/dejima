package main

import "testing"

// The empty-fleet call to action is GOLD, and it is the same gold as every
// other "this wants you" state.
//
// Asserted against styleNeedsYou rather than a literal hex: the point is that
// the two stay ONE visual idea. Pinning the string here would let them drift
// apart while both tests still passed.
func TestFirstRunRowUsesTheCallToActionGold(t *testing.T) {
	if got, want := styleFirstRun.GetForeground(), styleNeedsYou.GetForeground(); got != want {
		t.Errorf("first-run row foreground = %v, want the call-to-action gold %v", got, want)
	}
	if !styleFirstRun.GetBold() {
		t.Error("the call-to-action state is bold everywhere else; this one is not")
	}
	// The highlight must survive. The row is genuinely selected and Enter already
	// works — without the background it read as decoration, which is the bug the
	// selected styling was introduced to fix. Color emphasises it; it does not
	// replace the selection signal.
	if got, want := styleFirstRun.GetBackground(), styleSelected.GetBackground(); got != want {
		t.Errorf("first-run row background = %v, want the selected row's %v — "+
			"losing it makes the row read as a heading again", got, want)
	}
}
