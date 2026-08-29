package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A 401 is unambiguous — GitHub rejected a token we SENT — and it used to render
// as "github releases: HTTP 401", which sends the reader looking for an outage.
//
// It cost a real operator two detours: they refreshed `gh auth login`, which the
// daemon does not read, and the check kept failing with no hint that there are
// two credential stores and only one of them is consulted here.
func TestUnauthorizedNamesTheRightStore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	prev := releasesURL
	releasesURL = srv.URL
	t.Cleanup(func() { releasesURL = prev })

	_, err := LatestReleaseInfo(context.Background())
	if err == nil {
		t.Fatal("a 401 must be an error")
	}
	msg := err.Error()
	for _, want := range []string{
		"dejima github connect", // the remedy, not just the diagnosis
		"gh auth login",         // the thing that does NOT fix it
		"needs no auth",         // why an expired token BREAKS a working check
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("401 message is missing %q — it reads:\n%s", want, msg)
		}
	}
}
