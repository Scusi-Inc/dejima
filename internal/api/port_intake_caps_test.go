package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The refusal must state BOTH axes whichever one tripped.
//
// It used to be two refusals naming one number each, so raising max_files on a
// large tree bought you the max_bytes refusal on the next attempt — two round
// trips over a tree the walk had already measured completely. This is the whole
// point of the change: one refusal is enough to decide with.
func TestCapRefusalNamesBothAxes(t *testing.T) {
	lim := intakeLimits{maxFiles: 2000, maxBytes: 512 << 20}

	// Over on files only. The message must STILL carry the byte total, or the
	// operator raises max-files and learns about the size cap on the next trip.
	msg := capRefusal("vault", 4213, 300<<20, lim)
	if msg == "" {
		t.Fatal("4213 files against a 2000 cap was allowed")
	}
	for _, want := range []string{"4213", "300.0 MiB", "2000", "512.0 MiB", "vault"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal is missing %q — the operator has to guess it\n%s", want, msg)
		}
	}

	// Over on bytes only: same requirement in the other direction.
	msg = capRefusal("vault", 12, 900<<20, lim)
	if msg == "" {
		t.Fatal("900 MiB against a 512 MiB cap was allowed")
	}
	if !strings.Contains(msg, "12 files") {
		t.Errorf("a size refusal does not say how many files it is\n%s", msg)
	}
}

// The remedy must name the knob for BOTH surfaces. The daemon cannot know
// whether an API client or the CLI is asking, and a reader given only one of
// the two names has to go looking for the other — which is the state this
// change is fixing: the message named max_files, and no CLI flag existed.
func TestCapRefusalNamesTheKnob(t *testing.T) {
	msg := capRefusal("x", 9999, 1, intakeLimits{maxFiles: 10, maxBytes: 1 << 40})
	for _, want := range []string{"max_files", "max_bytes", "--max-files", "--max-bytes", "subdirectory"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal never mentions %q\n%s", want, msg)
		}
	}
}

func TestCapRefusalAllowsAtTheLimit(t *testing.T) {
	lim := intakeLimits{maxFiles: 2000, maxBytes: 512 << 20}
	if msg := capRefusal("x", 2000, 512<<20, lim); msg != "" {
		t.Errorf("exactly at the cap was refused: %s", msg)
	}
	if msg := capRefusal("x", 2001, 0, lim); msg == "" {
		t.Error("one over the file cap was allowed")
	}
	if msg := capRefusal("x", 0, (512<<20)+1, lim); msg == "" {
		t.Error("one byte over the size cap was allowed")
	}
}

// A raised cap must actually reach the walk. intakeLimitsFrom is the join
// between the decoded request and the check; if it dropped the override the
// flags would be accepted, sent, and ignored.
func TestIntakeLimitsOverrideReachesTheCheck(t *testing.T) {
	lim := intakeLimits{maxFiles: 5000, maxBytes: 2 << 30}
	if msg := capRefusal("x", 4213, 1<<30, lim); msg != "" {
		t.Errorf("a raised cap still refused: %s", msg)
	}
}

// THE BUG THIS CHANGE EXISTS TO FIX, pinned.
//
// PortIntakeRequest carried MaxFiles/MaxBytes and the daemon honoured them, but
// the client built the request without them — so the refusal told the operator
// to "raise it with max_files" and no surface could. The field existing is not
// the same as the field being SENT, and nothing asserted the difference. This
// reads the wire.
func TestPortIntakeRecursiveSendsTheCaps(t *testing.T) {
	var got PortIntakeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeJSON(w, http.StatusOK, PortIntakeResponse{Scope: "vault"})
	}))
	defer srv.Close()

	c, err := NewTCPClient(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := c.PortIntakeRecursive(context.Background(), "isl", "vault", "daily", "", true,
		PortIntakeCaps{MaxFiles: 5000, MaxBytes: 2 << 30}); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if got.MaxFiles != 5000 {
		t.Errorf("max_files on the wire = %d, want 5000 (the flag is accepted and dropped)", got.MaxFiles)
	}
	if got.MaxBytes != 2<<30 {
		t.Errorf("max_bytes on the wire = %d, want %d", got.MaxBytes, int64(2)<<30)
	}
	if !got.Recursive {
		t.Error("recursive was lost")
	}
}

// Zero must stay ABSENT from the body rather than travelling as an explicit 0.
// Both mean "default" to today's daemon, but only omission keeps saying so if
// the server ever distinguishes "unset" from "zero".
func TestPortIntakeRecursiveOmitsUnsetCaps(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		writeJSON(w, http.StatusOK, PortIntakeResponse{})
	}))
	defer srv.Close()

	c, err := NewTCPClient(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := c.PortIntakeRecursive(context.Background(), "isl", "vault", "daily", "", true,
		PortIntakeCaps{}); err != nil {
		t.Fatalf("intake: %v", err)
	}
	if _, ok := raw["max_files"]; ok {
		t.Error("max_files was sent when unset")
	}
	if _, ok := raw["max_bytes"]; ok {
		t.Error("max_bytes was sent when unset")
	}
}
