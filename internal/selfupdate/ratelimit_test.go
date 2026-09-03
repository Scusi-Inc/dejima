package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveStatus stands up a fake GitHub releases endpoint returning code with the
// given headers, and points LatestRelease at it.
func serveStatus(t *testing.T, code int, headers map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)

	prev := releasesURL
	releasesURL = srv.URL
	t.Cleanup(func() { releasesURL = prev })
}

// An exhausted anonymous limit should say so, say it's only the CHECK that is
// blocked, and say when it clears — "retry shortly" leaves the operator
// guessing, and a bare HTTP 403 reads like the update itself is broken.
func TestRateLimitExhaustedReportsReset(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	reset := time.Now().Add(37 * time.Minute).Unix()
	serveStatus(t, http.StatusForbidden, map[string]string{
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     fmt.Sprint(reset),
	})

	_, err := LatestRelease(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"rate limit", "resets in", "only the update CHECK"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q; got %q", want, msg)
		}
	}
}

// Without a reset header there is nothing to promise, so fall back to vaguer
// wording rather than inventing a time.
func TestRateLimitWithoutResetHeader(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	serveStatus(t, http.StatusTooManyRequests, map[string]string{"X-RateLimit-Remaining": "0"})

	_, err := LatestRelease(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "retry shortly") {
		t.Errorf("expected a fallback hint; got %q", err.Error())
	}
	if strings.Contains(err.Error(), "resets in") {
		t.Errorf("must not claim a reset time it doesn't know; got %q", err.Error())
	}
}

// A 403 with quota REMAINING is not exhaustion. Telling that operator to wait
// sends them to sit out a limit that was never the problem — with a token set,
// the likely cause is the token itself.
func TestForbiddenWithQuotaLeftIsNotRateLimit(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "bad-token")
	serveStatus(t, http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "58"})

	_, err := LatestRelease(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "rate limit") {
		t.Errorf("should not blame the rate limit when quota remains; got %q", msg)
	}
	if !strings.Contains(msg, "GITHUB_TOKEN") {
		t.Errorf("should point at the token as the likely cause; got %q", msg)
	}
}
