package selfupdate

import "testing"

// githubToken prefers an explicit env token, then the connected-identity fallback
// (so a daemon with a GitHub identity authenticates its update checks instead of
// sharing the anonymous 60/hr-per-IP limit that a burst of releases exhausts).
func TestGithubTokenFallback(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	old := TokenFallback
	t.Cleanup(func() { TokenFallback = old })

	TokenFallback = func() string { return "identity-tok" }
	if got := githubToken(); got != "identity-tok" {
		t.Errorf("no env token → fallback should be used, got %q", got)
	}

	t.Setenv("GITHUB_TOKEN", "env-tok")
	if got := githubToken(); got != "env-tok" {
		t.Errorf("an explicit env token must win over the fallback, got %q", got)
	}

	t.Setenv("GITHUB_TOKEN", "")
	TokenFallback = nil
	if got := githubToken(); got != "" {
		t.Errorf("no env, no fallback → empty (anonymous), got %q", got)
	}
}
