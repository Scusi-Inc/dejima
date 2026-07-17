package api

import (
	"context"
	"strings"

	"github.com/aoos/dejima/internal/project"
)

// GitHub identity tenancy — the API-layer glue for owner-scoped identities.
// Inside githubid the host tenant is represented as "" (legacy identities
// already are); islands and tokens carry project.HostOwner(). ghOwner translates
// at the boundary so githubid never needs the host owner's label.

// ghOwner maps an island/caller owner label to the githubid tenant key: "" for
// the host owner (so it lines up with legacy host identities), the tenant string
// otherwise.
func ghOwner(owner string) string {
	if strings.TrimSpace(owner) == "" || owner == project.HostOwner() {
		return ""
	}
	return owner
}

// callerGHScope returns the authenticated caller's githubid tenant key and
// whether they're the host owner (OwnsAll). A request with no identity (trusted
// local socket) is the host owner.
func (s *Server) callerGHScope(ctx context.Context) (owner string, ownsAll bool) {
	if id, ok := IdentityFromContext(ctx); ok {
		return ghOwner(strings.TrimSpace(id.Owner)), id.OwnsAll()
	}
	return "", true
}
