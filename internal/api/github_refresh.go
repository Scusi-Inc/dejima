package api

import (
	"github.com/aoos/dejima/internal/project"
)

// Refreshing every island's materialized gh credential when the identity store
// changes — the missing sibling of refreshIslandSecrets.
//
// An island does not read the host's ~/.config/gh. islandGHConfigDir writes a
// per-island hosts.yml from the resolved identity and that DIRECTORY is
// bind-mounted read-only at /opt/host/gh-config. It is written in
// credentialBindMounts, which runs at CONTAINER CREATE and nowhere else.
//
// So connecting a new identity, or repointing the default, updated the store and
// left every existing island holding the credential it was created with. When a
// token expires, the operator refreshes it host-side, `dejima github ls` shows
// the new one, and every island keeps failing with "Bad credentials" against a
// hosts.yml untouched since the day its container was made. That is exactly what
// happened, across several islands at once, and nothing connected the two facts.
//
// The mount is a DIRECTORY, so rewriting the file inside it is visible to a
// RUNNING container immediately — no recreate. (The secrets bug was the same
// shape with one extra twist: it mounted the FILE, so a rename replaced the
// inode and even a rewrite could not be seen. That is why this needs only the
// refresh half of that fix.)
//
// Best-effort by design: the identity IS stored, and a stale mount is a smaller
// problem than failing a write that already succeeded. Same call as secrets.
func (s *Server) refreshIslandGitHubConfigs() {
	projects, err := project.List()
	if err != nil {
		s.log.Warn("github identity changed but islands could not be listed", "err", err)
		return
	}
	for _, p := range projects {
		// Re-materialize from the store. An island that resolves no identity is
		// left alone: it is on the host fallback or has none, and writing an
		// empty config would take a working island to a broken one.
		if _, err := islandGHConfigDir(p); err != nil {
			s.log.Warn("island gh credential not refreshed",
				"island", p.Name, "err", err)
		}
		// The commit-author gitconfig is materialized from the SAME identity and
		// was equally stale. Refreshing only the credential would leave an island
		// pushing AS the new identity while committing as the old one's noreply
		// email — so GitHub authenticates one account and attributes the commits
		// to another. islandGitConfig's own comment exists to prevent exactly
		// that, and refreshing half the pair reintroduces it in a subtler form:
		// the push succeeds, so nothing looks wrong.
		if _, err := islandGitConfig(p); err != nil {
			s.log.Warn("island git author config not refreshed",
				"island", p.Name, "err", err)
		}
	}
}
