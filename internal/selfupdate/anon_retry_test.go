package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An expired token must not break a check that never needed one.
//
// The check reads a PUBLIC release; the token only lifts GitHub's anonymous
// 60/hr limit. Wiring it to a credential (bf5a452) meant an expired token turned
// a working 200 into a 401 — so having a BAD credential became worse than having
// none, and an operator lost update checks for a reason unconnected to updates.
// That happened in the field: hundreds of updates while anonymous, then a break
// a month after the coupling landed.
func TestExpiredTokenFallsBackToAnonymous(t *testing.T) {
	var sawAuthed, sawAnon bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuthed = true
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sawAnon = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","body":"notes","html_url":"http://x"}`))
	}))
	defer srv.Close()
	prev, prevTok := releasesURL, TokenFallback
	releasesURL = srv.URL
	TokenFallback = func() string { return "expired-token" }
	t.Cleanup(func() { releasesURL, TokenFallback = prev, prevTok })

	info, err := LatestReleaseInfo(context.Background())
	if err != nil {
		t.Fatalf("an expired token must degrade to anonymous, not fail: %v", err)
	}
	if info.Tag != "v9.9.9" {
		t.Errorf("tag = %q, want the release the anonymous call returned", info.Tag)
	}
	if !sawAuthed || !sawAnon {
		t.Errorf("expected an authed attempt THEN an anonymous retry; authed=%v anon=%v\n"+
			"if only one fired, the retry is not happening and this test is vacuous",
			sawAuthed, sawAnon)
	}
}

// When the anonymous retry ALSO fails, the 401 is real and must still surface —
// degrading gracefully must not mean swallowing a genuine problem.
func TestAnonymousRetryFailingStillReports(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	prev, prevTok := releasesURL, TokenFallback
	releasesURL = srv.URL
	TokenFallback = func() string { return "expired-token" }
	t.Cleanup(func() { releasesURL, TokenFallback = prev, prevTok })

	_, err := LatestReleaseInfo(context.Background())
	if err == nil {
		t.Fatal("a 401 that survives the anonymous retry must still be an error")
	}
	if !strings.Contains(err.Error(), "github connect") {
		t.Errorf("the surviving error should still name the remedy, got: %v", err)
	}
}
