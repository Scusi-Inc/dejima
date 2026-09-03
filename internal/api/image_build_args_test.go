package api

import (
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/version"
)

// The island image's in-island `dejima` CLI is fetched inside a RUN that resolves
// DEJIMA_VERSION=latest by curl-ing the GitHub releases API. Docker cannot see
// that the answer changed, so it reuses that layer forever: the in-island CLI
// froze at whatever release was newest the first time the layer built, and no
// number of `dejima image build` runs moved it (observed in the wild pinned at
// v0.8.59 while the daemon had moved on). Passing the daemon's own version as an
// explicit --build-arg changes the ARG, which invalidates the layer — so this
// test guards a cache-invalidation property, not just a flag.
func TestImageBuildPinsInIslandCLIToDaemonRelease(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)

	version.Version = "v9.8.7"
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/image/build", ""); !ok2xx(rr.Code) {
		t.Fatalf("POST /v1/image/build: %d, body %s", rr.Code, rr.Body.String())
	}
	if f.buildCalls != 1 {
		t.Fatalf("expected exactly one build, got %d", f.buildCalls)
	}
	if got := f.lastBuildArgs["DEJIMA_VERSION"]; got != "v9.8.7" {
		t.Errorf("DEJIMA_VERSION = %q, want the daemon's release v9.8.7", got)
	}
}

// A dev/source daemon has no published release to match, so it must NOT pin:
// there is no dejima_dev_linux_*.tar.gz asset, and pinning to "dev" would make
// the Dockerfile's download step fail outright rather than fall back.
func TestImageBuildDoesNotPinForDevDaemon(t *testing.T) {
	defer func(v string) { version.Version = v }(version.Version)

	for _, v := range []string{"dev", "v0.8.60-3-gabc1234"} {
		version.Version = v
		h, f := newTestServer(t)
		if rr := do(t, h, http.MethodPost, "/v1/image/build", ""); !ok2xx(rr.Code) {
			t.Fatalf("version %q: POST /v1/image/build: %d", v, rr.Code)
		}
		if got, ok := f.lastBuildArgs["DEJIMA_VERSION"]; ok {
			t.Errorf("version %q: pinned DEJIMA_VERSION=%q; a non-release must keep the \"latest\" default", v, got)
		}
	}
}
