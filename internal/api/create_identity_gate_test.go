package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestBlockDoomedClone covers the gate decision without network: the probe is
// stubbed and its call is recorded so we also assert it runs ONLY in the risky
// case.
func TestBlockDoomedClone(t *testing.T) {
	srv, _, _ := wakeServer(t) // HOME is a temp dir → no daemon GitHub identities

	cases := []struct {
		name       string
		req        CreateIslandRequest
		anon       bool // what the stubbed probe returns
		wantGate   bool // expect a non-nil (blocking) error
		wantProbed bool // expect the anon probe to have been called
	}{
		{"remote no-identity not-anon → gate", CreateIslandRequest{Repo: "https://github.com/o/r"}, false, true, true},
		{"remote no-identity anon-reachable → ok", CreateIslandRequest{Repo: "https://github.com/o/r"}, true, false, true},
		{"override --force → ok, no probe", CreateIslandRequest{Repo: "https://github.com/o/r", AllowNoIdentity: true}, false, false, false},
		{"named identity → ok, no probe", CreateIslandRequest{Repo: "https://github.com/o/r", GitHubIdentity: "me"}, false, false, false},
		{"local path → ok, no probe", CreateIslandRequest{Repo: "/home/me/proj"}, false, false, false},
		{"scp-style url, not-anon → gate", CreateIslandRequest{Repo: "git@github.com:o/r.git"}, false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probed := false
			srv.anonCloneFn = func(context.Context, string) bool { probed = true; return c.anon }
			err := srv.blockDoomedClone(context.Background(), c.req)
			if (err != nil) != c.wantGate {
				t.Errorf("blockDoomedClone gate = %v (err=%v), want %v", err != nil, err, c.wantGate)
			}
			if probed != c.wantProbed {
				t.Errorf("probe called = %v, want %v", probed, c.wantProbed)
			}
			// The gate is the first wall a new operator hits, so the message must
			// carry a runnable remedy — and the client matches the leading phrase
			// to upgrade it into the guided TUI step.
			if err != nil {
				if !strings.Contains(err.Error(), "github connect") {
					t.Errorf("gate error should carry the remedy; got %q", err.Error())
				}
				if !strings.Contains(err.Error(), "needs a GitHub identity to clone") {
					t.Errorf("gate error lost the phrase the TUI matches on; got %q", err.Error())
				}
			}
		})
	}
}

// TestGateSchemeAllowlist: only real git-remote schemes are probed; a file:// (or
// other non-git) URL is gated WITHOUT ever shelling ls-remote at it.
func TestGateSchemeAllowlist(t *testing.T) {
	srv, _, _ := wakeServer(t)

	// isGitRemoteURL truth table.
	for _, u := range []string{"https://github.com/o/r", "http://x/y", "git://x/y", "ssh://git@x/y", "git@github.com:o/r.git"} {
		if !isGitRemoteURL(u) {
			t.Errorf("isGitRemoteURL(%q) = false, want true", u)
		}
	}
	for _, u := range []string{"file:///etc/passwd", "file://./repo", "ftp://x/y", "/local/path", "weird://z"} {
		if isGitRemoteURL(u) {
			t.Errorf("isGitRemoteURL(%q) = true, want false", u)
		}
	}

	// A file:// URL (IsURL true, but not a git remote): gated, and the probe is
	// never called.
	probed := false
	srv.anonCloneFn = func(context.Context, string) bool { probed = true; return true }
	if err := srv.blockDoomedClone(context.Background(), CreateIslandRequest{Repo: "file:///srv/secret-repo"}); err == nil {
		t.Error("a file:// repo with no identity should be gated")
	}
	if probed {
		t.Error("the anon probe must NOT run for a non-git-remote scheme (file://)")
	}

	// An https URL still gets probed.
	probed = false
	srv.anonCloneFn = func(context.Context, string) bool { probed = true; return true }
	if err := srv.blockDoomedClone(context.Background(), CreateIslandRequest{Repo: "https://github.com/o/r"}); err != nil {
		t.Errorf("anon-reachable https repo should pass: %v", err)
	}
	if !probed {
		t.Error("the anon probe should run for an https git remote")
	}
}

// TestCreateIslandIdentityGate exercises the gate through the HTTP create handler:
// a private-ish URL repo with no identity is refused with the remedy, and --force
// (allow_no_identity) lets it through.
func TestCreateIslandIdentityGate(t *testing.T) {
	srv, h, _ := wakeServer(t)
	srv.anonCloneFn = func(context.Context, string) bool { return false } // simulate a not-anon-reachable repo

	// No identity, no override → refused with the actionable remedy.
	rr := do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"https://github.com/acme/private","name":"gated","agent":"claude-code"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("gated create = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "github connect") {
		t.Errorf("gate response missing the remedy: %s", rr.Body.String())
	}

	// Same create with --force (allow_no_identity) proceeds.
	rr = do(t, h, http.MethodPost, "/v1/islands",
		`{"repo":"https://github.com/acme/private","name":"forced","agent":"claude-code","allow_no_identity":true}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("forced create = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
}
