package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// markSetupDoneIfHealthy must NOT write the dismissal marker when the daemon
// isn't reachable — otherwise a half-finished setup gets cached and the first-run
// prompt never returns (the dejimaqa stranding bug). With a temp HOME and no
// dejimad listening, health fails, so the marker must be absent afterward.
func TestMarkSetupDoneIfHealthy_NoDaemonLeavesNoMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DEJIMA_HOST", "") // force the local-socket target
	t.Setenv("DEJIMA_TOKEN", "")

	if ok := markSetupDoneIfHealthy(context.Background()); ok {
		t.Fatal("markSetupDoneIfHealthy reported success with no daemon running")
	}

	marker := filepath.Join(home, ".dejima", "onboarding-dismissed")
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("dismissal marker was written despite an unreachable daemon (%s)", marker)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stat-ing marker: %v", err)
	}
}
