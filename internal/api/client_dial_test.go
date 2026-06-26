package api

import (
	"errors"
	"net/http"
	"testing"
)

// dialErr must tag only a positive gone response (404/410) as ErrSessionGone;
// a transport failure (nil response) or a server hiccup (5xx) must NOT be — those
// are retryable, since the tmux session survives on the daemon.
func TestDialErrClassifies(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusGone} {
		if err := dialErr("dial session", &http.Response{StatusCode: code}, errors.New("handshake")); !errors.Is(err, ErrSessionGone) {
			t.Errorf("status %d → want ErrSessionGone, got %v", code, err)
		}
	}

	base := errors.New("connection refused")
	transport := dialErr("dial session", nil, base) // no handshake response at all
	if errors.Is(transport, ErrSessionGone) {
		t.Errorf("transport error must not be ErrSessionGone: %v", transport)
	}
	if !errors.Is(transport, base) {
		t.Errorf("transport error should wrap the original: %v", transport)
	}

	if err := dialErr("dial session", &http.Response{StatusCode: http.StatusServiceUnavailable}, base); errors.Is(err, ErrSessionGone) {
		t.Errorf("503 must be retryable, not session-gone: %v", err)
	}
}
