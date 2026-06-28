package api

import (
	"net/http"
	"testing"
)

// TestWriteFileLandsAgentReadable verifies a file copied into an island via the
// write path is world-readable (0644) on the source, so the in-island agent
// (uid 1000) can read it without an in-container chmod — the P0 fix for the
// image-input flow. `docker cp` preserves the source mode, so asserting the
// source mode is the right check.
func TestWriteFileLandsAgentReadable(t *testing.T) {
	h, f := newTestServer(t)
	if rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"r","name":"alpha","agent":"claude-code"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: got %d", rr.Code)
	}

	rr := do(t, h, http.MethodPut, "/v1/islands/alpha/files/workspace/x.png", "fake-image-bytes")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("write file: got %d, want 204", rr.Code)
	}
	if f.lastCopyMode != 0o644 {
		t.Errorf("copied source mode = %o, want 0644 (so uid 1000 can read it)", f.lastCopyMode)
	}
}
