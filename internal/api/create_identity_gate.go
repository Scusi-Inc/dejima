package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/reposrc"
)

// Create-time identity gate — stop a doomed private-repo clone before it
// launches. A remote repo with NO GitHub identity that isn't anonymously
// cloneable would clone-fail on boot and leave an empty, repo-less island (the
// auth failure only surfaces later, e.g. as a 2-minute "provisioning" hang on
// connect). We catch it at create with the exact remedy, but allow an explicit
// override for the deliberate "create now, authenticate later" case.
//
// Detection deliberately does NOT try to decide "is this repo private" — a
// private repo is indistinguishable from a nonexistent one to an unauthenticated
// probe (both 404). Instead: gate only when a remote repo has no identity AND a
// truly anonymous probe can't reach it. Public/anon-reachable repos always pass.

// gateProbeTimeout bounds the anonymous-reachability probe so a slow/hanging
// remote can't stall a create.
const gateProbeTimeout = 12 * time.Second

// blockDoomedClone returns a non-nil, actionable error when the create should be
// refused because the island would clone a remote repo with no GitHub identity
// and the repo isn't anonymously reachable. Returns nil (proceed) for: an
// override, a local/seed source, an island that has an identity, or a repo the
// anonymous probe can reach. The probe (s.anonCloneFn) runs ONLY in the risky
// case, so a public repo or an identity-bound create pays no cost.
func (s *Server) blockDoomedClone(ctx context.Context, req CreateIslandRequest) error {
	repo := strings.TrimSpace(req.Repo)
	if req.AllowNoIdentity || !reposrc.IsURL(repo) || s.islandWillHaveGitHubIdentity(req.GitHubIdentity) {
		return nil
	}
	// Only probe real git-remote schemes. A file:// or other exotic URL is treated
	// as not anonymously reachable → gate, so the daemon never points `git
	// ls-remote` at a caller-supplied local path or non-git scheme (closes the
	// file:// local-path probe vector).
	if isGitRemoteURL(repo) && s.anonCloneFn(ctx, repo) {
		return nil // public / anonymously reachable — no identity needed
	}
	// Phrased as steps, not prose: this is the first wall a new operator hits,
	// and the old single-sentence version buried the actual command. The client
	// matches on "needs a GitHub identity to clone" to upgrade this into the
	// guided TUI step — keep that substring intact.
	return fmt.Errorf("%q needs a GitHub identity to clone (it isn't anonymously reachable), and none is configured.\n"+
		"  1. On a machine where the gh CLI can see the repo, sign in:  gh auth login\n"+
		"  2. Connect it to this daemon:                                dejima github connect\n"+
		"  3. Retry creating the island.\n"+
		"Or pass --force to create it now and authenticate later.", repo)
}

// islandWillHaveGitHubIdentity reports whether the island has SOME identity to
// clone with: an explicitly named one, or at least one configured daemon
// identity (the default a create falls back to).
func (s *Server) islandWillHaveGitHubIdentity(named string) bool {
	if strings.TrimSpace(named) != "" {
		return true
	}
	store, err := githubid.Load()
	return err == nil && len(store.Identities) > 0
}

// isGitRemoteURL reports whether url is a network git remote we should probe —
// an https/http/git/ssh scheme, or scp-style user@host:path. Everything else
// (notably file://) is NOT a network remote, so the gate treats it as
// not-anon-reachable rather than shelling `git ls-remote` at it.
func isGitRemoteURL(url string) bool {
	for _, p := range []string{"https://", "http://", "git://", "ssh://"} {
		if strings.HasPrefix(url, p) {
			return true
		}
	}
	// scp-style: user@host:path — an "@" in the part before the first ":" and no
	// slash in that host segment (mirrors reposrc.IsURL's scp detection).
	if i := strings.Index(url, ":"); i > 0 {
		host := url[:i]
		if strings.Contains(host, "@") && !strings.Contains(host, "/") {
			return true
		}
	}
	return false
}

// repoAnonCloneable probes whether url is reachable WITHOUT credentials. It
// disables git credential helpers and interactive prompts so it's a TRUE
// anonymous test — a host credential helper must not make a private repo look
// public (which would let a doomed clone through). Non-zero exit
// (auth-required / not-found / unreachable) → false.
func repoAnonCloneable(ctx context.Context, url string) bool {
	pctx, cancel := context.WithTimeout(ctx, gateProbeTimeout)
	defer cancel()
	// An empty `credential.helper` clears the helper list, so no stored creds are
	// offered; GIT_TERMINAL_PROMPT=0 stops git from prompting for a password.
	cmd := exec.CommandContext(pctx, "git", "-c", "credential.helper=", "ls-remote", "--heads", url)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	return cmd.Run() == nil
}
