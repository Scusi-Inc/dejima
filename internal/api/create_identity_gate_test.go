package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/githubid"
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

// TestGatePassesWithConfiguredIdentity is the regression guard for the bug where
// the gate read the legacy store.Identities map (always nil after Load migrates
// it into Idents), so it demanded a token even on daemons WITH a connected
// identity. A default identity must let a private-repo create through untouched.
func TestGatePassesWithConfiguredIdentity(t *testing.T) {
	srv, _, _ := wakeServer(t)                                            // temp HOME → empty store to start
	srv.anonCloneFn = func(context.Context, string) bool { return false } // not anon-reachable

	// With no identity, the private clone is gated.
	if err := srv.blockDoomedClone(context.Background(),
		CreateIslandRequest{Repo: "https://github.com/acme/private"}); err == nil {
		t.Fatal("expected a gate with no identity configured")
	}

	// Connect a host identity and make it the default — exactly what
	// `dejima github connect` does. The gate must now pass without a token prompt.
	if _, err := githubid.Update(func(s *githubid.Store) error {
		s.Put(githubid.Identity{Name: "github", Login: "octocat", Token: "tok"})
		return s.SetDefault("github")
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.blockDoomedClone(context.Background(),
		CreateIslandRequest{Repo: "https://github.com/acme/private"}); err != nil {
		t.Errorf("a configured default identity should clear the gate, got: %v", err)
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

// The deny-by-default gate must not read as a dead end for the host operator:
// after #334 there are two routes forward, and naming only the identity one
// leaves "why can't I clone" unanswered for anyone who deliberately wants their
// own login in the island.
func TestGateOffersHostCredentialToHostOperator(t *testing.T) {
	s := &Server{anonCloneFn: func(context.Context, string) bool { return false }}
	err := s.blockDoomedClone(context.Background(), CreateIslandRequest{
		Repo: "https://github.com/aoos/private.git", Name: "proj",
	})
	if err == nil {
		t.Fatal("an unreachable private repo with no identity must still be gated")
	}
	msg := err.Error()
	// The client keys the guided TUI step off this substring.
	if !strings.Contains(msg, "needs a GitHub identity to clone") {
		t.Errorf("the client's match substring must survive: %q", msg)
	}
	// Identity stays the primary remedy, listed first.
	if !strings.Contains(msg, "dejima github connect") {
		t.Errorf("the identity path must remain the headline remedy: %q", msg)
	}
	if !strings.Contains(msg, "dejima github host-credential grant proj") {
		t.Errorf("the host operator should be told the second route, named for this island: %q", msg)
	}
	// And told what it costs, so it isn't a casual choice.
	if !strings.Contains(msg, "every\nprivate repo") {
		t.Errorf("the wider route must state its cost: %q", msg)
	}
	if strings.Index(msg, "dejima github connect") > strings.Index(msg, "host-credential") {
		t.Error("the narrower remedy must come first")
	}
}
