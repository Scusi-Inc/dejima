package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
)

// withUnreachableDaemon points the first-run health probe at a stub that always
// fails, so detectFirstRunContext classifies purely on the configured target
// (env/profile) rather than on a live daemon. Restores the real client on cleanup.
func withUnreachableDaemon(t *testing.T) {
	t.Helper()
	orig := api_client
	api_client = func() (*api.Client, error) { return nil, errors.New("stub: daemon unreachable") }
	t.Cleanup(func() { api_client = orig })
}

// TestDetectFirstRunJoinedProfile is the #209 regression: a teammate who ran
// `dejima join <invite>` has a saved ACTIVE profile but no DEJIMA_HOST and no
// local daemon. When that host is momentarily unreachable, first-run must route
// to firstRunClientUnreachable (troubleshoot → dashboard), NOT firstRunFreshHost
// (the "set up a server" question). Before the fix it fell through to FreshHost
// because the classifier consulted only daemon-reachability + DEJIMA_HOST, never
// the saved profile.
func TestDetectFirstRunJoinedProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_HOST", "") // the whole point of join: no env target
	withUnreachableDaemon(t)

	if _, _, err := joinFromInvite(goldenInvite); err != nil {
		t.Fatalf("joinFromInvite(golden) errored: %v", err)
	}
	// Precondition: the join really did create an active profile.
	if cfg, _ := clientcfg.Load(); cfg.ActiveProfile == "" {
		t.Fatal("precondition: join should have set an active profile")
	}

	if got := detectFirstRunContext(context.Background()); got != firstRunClientUnreachable {
		t.Errorf("joined-but-unreachable teammate = %v, want firstRunClientUnreachable (%v); a saved active profile must never route to the set-up-a-server question",
			got, firstRunClientUnreachable)
	}
}

// TestDetectFirstRunNoTargetNotClient guards the other side: with NO saved
// profile and NO DEJIMA_HOST, an unreachable daemon must NOT be misread as a
// configured client — it's a fresh machine (FreshHost on macOS, Generic
// elsewhere). This pins that the #209 fix doesn't over-broaden ClientUnreachable.
func TestDetectFirstRunNoTargetNotClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_HOST", "")
	withUnreachableDaemon(t)

	if got := detectFirstRunContext(context.Background()); got == firstRunClientUnreachable || got == firstRunConfigured {
		t.Errorf("no target + unreachable = %v, want FreshHost/Generic (not a configured client)", got)
	}
}
