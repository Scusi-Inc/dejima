package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/runtime"
)

// The gap this closes, stated as the operator hit it: they rebuilt the island
// image, created a codex agent into an existing island, and got a dead agent.
// Nothing on any surface said the island was behind — because the only
// staleness signal compares the island's VERSION STAMP to the running daemon,
// and `dejima image build` moves the image without touching either. So they
// deleted and re-added the agent, which shares the same container and therefore
// the same old binary, and could not have worked.

// islandDetail fetches the detail payload, which is where ImageStale lives (two
// engine inspects is too much for the list view).
func islandDetail(t *testing.T, h http.Handler, name string) IslandInfo {
	t.Helper()
	rr := do(t, h, http.MethodGet, "/v1/islands/"+name, "")
	if !ok2xx(rr.Code) {
		t.Fatalf("GET island %s: %d, body %s", name, rr.Code, rr.Body.String())
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// A container built from an image the tag no longer points at is STALE.
func TestIslandOnAnOldImageReportsStale(t *testing.T) {
	h, f := newTestServer(t)
	seedIslandHTTP(t, h, "proj")
	f.containerImageID = "sha256:the-one-from-august"

	info := islandDetail(t, h, "proj")
	if info.ImageStale == nil {
		t.Fatal("no verdict at all for a container plainly on a different image — " +
			"absent means 'could not determine', which is not what happened here")
	}
	if !*info.ImageStale {
		t.Error("a container running an image other than the tag's current target " +
			"reported up to date — this is the signal whose absence cost an operator " +
			"an afternoon of deleting and re-adding agents")
	}
}

// The control: matching ids must NOT report stale, or the prompt becomes noise
// that everyone learns to ignore.
func TestIslandOnTheCurrentImageIsNotStale(t *testing.T) {
	h, _ := newTestServer(t)
	seedIslandHTTP(t, h, "proj")

	info := islandDetail(t, h, "proj")
	if info.ImageStale == nil {
		t.Fatal("no verdict for a container whose image matches; the check did not run")
	}
	if *info.ImageStale {
		t.Error("an island on the current image was told to upgrade")
	}
}

// "Could not look" must never render as "stale".
//
// An engine that will not answer knows nothing about staleness. Reporting that
// as "upgrade this island" puts a prompt on screen for an island that may be
// perfectly current, and — unlike the reverse — it never clears, so the operator
// learns to ignore the one signal we just added.
func TestAnUnreadableImageIDIsNotAStaleVerdict(t *testing.T) {
	h, f := newTestServer(t)
	seedIslandHTTP(t, h, "proj")
	f.containerImageErr = errors.New("engine says no")

	info := islandDetail(t, h, "proj")
	if info.ImageStale != nil {
		t.Errorf("a failed lookup produced a verdict (%v) — 'these differ' and "+
			"'I could not look' are different answers", *info.ImageStale)
	}
}

// A container that is not RUNNING is not stale: there is nothing running on the
// old image, and wake/create picks up the current one anyway. Flagging it would
// nag about an island that is about to fix itself.
func TestAStoppedIslandIsNotReportedStale(t *testing.T) {
	h, f := newTestServer(t)
	seedIslandHTTP(t, h, "proj")
	f.containerImageID = "sha256:the-one-from-august"
	f.mu.Lock()
	f.status = runtime.StatusExited
	f.mu.Unlock()

	info := islandDetail(t, h, "proj")
	if info.ImageStale != nil {
		t.Errorf("a stopped container got an image verdict (%v); nothing is running "+
			"on that image", *info.ImageStale)
	}
}
