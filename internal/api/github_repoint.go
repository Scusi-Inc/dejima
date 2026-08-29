package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/project"
)

// handleSetIslandGitHubIdentity repoints an island at a different stored
// identity (PUT /v1/islands/:name/github-identity). "" means follow the default.
//
// This is the level the daemon could not act on. It could ADD identities, LIST
// them, and choose a DEFAULT — but which identity an island uses was fixed at
// create time and only editable by hand-editing config.toml on the host. When
// eight islands were pinned to an identity whose token had expired, the only
// supported move was to refresh that exact identity; pointing them at the
// working one was not an operation the product had.
//
// The pin is written AND the credential is re-materialized in the same call.
// Writing the pin alone would leave the island holding the previous identity's
// token until something recreated the container — the change would report
// success and the island would keep failing, which is how this class of bug has
// gone wrong four times now.
func (s *Server) handleSetIslandGitHubIdentity(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req SetIslandGitHubIdentityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	want := strings.TrimSpace(req.Identity)

	store, err := githubid.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Refuse a pin the daemon cannot resolve, rather than writing it and letting
	// the island discover it has no credential at all. A dangling pin is
	// indistinguishable from an expired token from inside the island, so the
	// cheapest place to catch it is the moment someone asks for it.
	id, ok := store.ResolveForIsland(ghOwner(p.Owner), want)
	if !ok {
		if want == "" {
			writeError(w, http.StatusConflict, errors.New(
				"this daemon has no default GitHub identity, so following the default "+
					"would leave the island with no credential — set one with "+
					"`dejima github default <name>`, or name an identity here"))
			return
		}
		writeError(w, http.StatusNotFound, fmt.Errorf(
			"no GitHub identity %q is available to island %q — see `dejima github ls`", want, name))
		return
	}

	p.GitHubIdentity = want
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Both halves: the gh credential AND the commit-author config are derived
	// from this identity, and refreshing one without the other makes an island
	// push as one account and commit as another.
	if _, err := islandGHConfigDir(p); err != nil {
		s.log.Warn("island gh credential not refreshed after repoint", "island", name, "err", err)
	}
	if _, err := islandGitConfig(p); err != nil {
		s.log.Warn("island git author config not refreshed after repoint", "island", name, "err", err)
	}
	s.log.Info("island github identity repointed", "island", name, "identity", want, "resolved", id.Name)
	writeJSON(w, http.StatusOK, SetIslandGitHubIdentityResponse{
		Island: name, Identity: want, Resolved: id.Name, Login: id.Login,
	})
}
