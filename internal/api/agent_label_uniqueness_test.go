package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestAddAgentDedupesLabel asserts the create handler auto-increments a label
// that's already in use (it never 409s on a label) and returns the FINAL
// assigned label so the CLI/TUI can surface "named it build-2".
func TestAddAgentDedupesLabel(t *testing.T) {
	h, _ := newTestServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"git@github.com:me/proj.git","name":"proj","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d, body %s", rr.Code, rr.Body.String())
	}

	add := func(label string) AgentInfo {
		t.Helper()
		rr := do(t, h, http.MethodPost, "/v1/islands/proj/agents",
			`{"type":"claude-code","label":"`+label+`"}`)
		if rr.Code != http.StatusCreated {
			t.Fatalf("add agent %q: got %d, body %s", label, rr.Code, rr.Body.String())
		}
		var a AgentInfo
		if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}

	// First "build" is free.
	if a := add("build"); a.Label != "build" {
		t.Errorf("add build (free) → label %q, want build", a.Label)
	}
	// A second "build" collides → build-2.
	if a := add("build"); a.Label != "build-2" {
		t.Errorf("add build (taken) → label %q, want build-2", a.Label)
	}
	// Then Build-3: the collision check is case-insensitive (Build matches the
	// existing build/build-2), but the user's chosen casing is preserved.
	if a := add("Build"); a.Label != "Build-3" {
		t.Errorf("add Build (taken) → label %q, want Build-3", a.Label)
	}
	// A fresh label is returned as-is.
	if a := add("frontend"); a.Label != "frontend" {
		t.Errorf("add frontend (free) → label %q, want frontend", a.Label)
	}
	// An OMITTED label no longer stays blank: the daemon now derives a Type-based
	// default ("claude-code" → "claude") and dedupes it. The island's primary is a
	// claude-code agent already backfilled to "claude", so the first defaulted add
	// is "claude-2", and the next is "claude-3" — never blank, never a bare id.
	if a := add(""); a.Label != "claude-2" {
		t.Errorf("add empty → label %q, want claude-2 (Type-derived default, deduped)", a.Label)
	}
	if a := add(""); a.Label != "claude-3" {
		t.Errorf("add empty again → label %q, want claude-3", a.Label)
	}
}

// TestUpdateAgentDedupesLabel asserts the rename handler auto-increments a label
// that collides with a DIFFERENT agent, but treats renaming an agent to its own
// current label as a no-op (not "-2").
func TestUpdateAgentDedupesLabel(t *testing.T) {
	h, _ := newTestServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"git@github.com:me/proj.git","name":"proj","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d, body %s", rr.Code, rr.Body.String())
	}
	// One agent labeled "build", and a second with a distinct label.
	rr = do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"claude-code","label":"build"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add build agent: got %d, body %s", rr.Code, rr.Body.String())
	}
	rr = do(t, h, http.MethodPost, "/v1/islands/proj/agents", `{"type":"claude-code","label":"test"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("add agent: got %d, body %s", rr.Code, rr.Body.String())
	}
	var second AgentInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &second)

	rename := func(id, label string) AgentInfo {
		t.Helper()
		rr := do(t, h, http.MethodPatch, "/v1/islands/proj/agents/"+id, `{"label":"`+label+`"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("rename %s → %q: got %d, body %s", id, label, rr.Code, rr.Body.String())
		}
		var a AgentInfo
		if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}

	// Renaming the second agent onto "build" (held by the primary) auto-increments.
	if a := rename(second.ID, "build"); a.Label != "build-2" {
		t.Errorf("rename %s → build (taken) → %q, want build-2", second.ID, a.Label)
	}
	// Renaming it again to its own current label "build-2" is a no-op (not build-3).
	if a := rename(second.ID, "build-2"); a.Label != "build-2" {
		t.Errorf("rename to own label → %q, want build-2 (no-op)", a.Label)
	}
}

// TestDuplicateIslandStillConflicts is a regression guard: island NAMES remain
// unique (the create handler 409s on a duplicate). Agent-label uniqueness does
// not touch island creation; this asserts that contract still holds.
func TestDuplicateIslandStillConflicts(t *testing.T) {
	h, _ := newTestServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"git@github.com:me/proj.git","name":"proj","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create island: got %d, body %s", rr.Code, rr.Body.String())
	}
	rr = do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"git@github.com:me/proj.git","name":"proj","agent":"claude-code"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate island: got %d, want 409 (island names stay unique)", rr.Code)
	}
}
