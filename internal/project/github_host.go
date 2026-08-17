package project

import (
	"strings"
	"time"
)

// The host operator's own `gh` login is the widest-blast-radius credential
// Dejima hands an island: its read scope is the operator's whole account, so an
// island holding it can read every private repo that account can see. Write is
// separately scoped by the token itself, so the exposure is exfiltration rather
// than tampering — but "every private repo" is a large enough surface that it
// gets the same treatment as Port, capability, MCP, link and spawn: deny by
// default, allowed only by an explicit operator grant.
//
// It used to be inherited silently by any host-owned island, on the reasoning
// that your own island is you. That held when an island was a place you worked;
// it stops holding when an island runs several autonomous agents, which is the
// one case where "it's just me in here" is false. A per-island identity
// (GitHubIdentity, see internal/githubid) still takes precedence over this and
// is the better answer — this grant only governs the fallback to the host's own
// login.

// HostGitHubGrant records a decision to let one island use the HOST operator's
// own ~/.config/gh. Its presence IS the grant; nil means denied.
type HostGitHubGrant struct {
	GrantedAt time.Time `toml:"granted_at"`
	// GrantedBy is the actor label from the API identity, when known. Empty for
	// the trusted local socket (the host operator, unauthenticated by design).
	GrantedBy string `toml:"granted_by,omitempty"`
	// Grandfathered marks a grant written by the deny-by-default migration
	// rather than chosen by an operator. It behaves identically — the point is
	// that it can be TOLD APART, so "islands still carrying the old inherited
	// credential" is a question with an answer, and so surfaces can nag about it
	// without nagging about deliberate grants.
	Grandfathered bool `toml:"grandfathered,omitempty"`
}

// HostGitHubAllowed reports whether this island may mount the host operator's
// own gh credential. Deny-by-default: no grant, no credential.
func (p *Project) HostGitHubAllowed() bool { return p.HostGitHubGrant != nil }

// IsHostOwned reports whether this island belongs to the host operator rather
// than a tenant. Ownership is backfilled on Load, so an empty owner only occurs
// on a Project that hasn't been through it yet.
func (p *Project) IsHostOwned() bool {
	owner := strings.TrimSpace(p.Owner)
	return owner == "" || owner == HostOwner()
}

// GrantHostGitHub records an explicit operator grant, replacing any existing one
// (which also clears the Grandfathered marker — an operator re-granting is a
// deliberate decision, and should stop being reported as leftover migration
// state). Returns the stored grant.
func (p *Project) GrantHostGitHub(by string, now time.Time) *HostGitHubGrant {
	p.HostGitHubGrant = &HostGitHubGrant{
		GrantedAt: now.UTC(),
		GrantedBy: strings.TrimSpace(by),
	}
	// A grant is a decision, so it also settles the migration question — a later
	// revoke must not be undone by re-grandfathering on the next Load.
	p.HostGitHubReviewed = true
	return p.HostGitHubGrant
}

// RevokeHostGitHub drops the grant. Returns the removed grant and whether there
// was one. The island keeps working; it just falls back to having no GitHub
// credential, which surfaces the same way it does for a tenant island.
func (p *Project) RevokeHostGitHub() (*HostGitHubGrant, bool) {
	if p.HostGitHubGrant == nil {
		return nil, false
	}
	removed := p.HostGitHubGrant
	p.HostGitHubGrant = nil
	// Revoking is a decision too: without this the load-time migration would
	// re-grant on the next read and the revoke would silently undo itself.
	p.HostGitHubReviewed = true
	return removed, true
}

// migrateHostGitHub is the one-time deny-by-default cutover, run from Load.
//
// Every host-owned island that predates the grant model was relying on the
// silent inheritance, so a hard cutover would break clone/push across an
// operator's whole fleet on upgrade, with no error that names the cause. Instead
// each such island is grandfathered into an explicit grant — same access as
// before, but now visible in the grants surfaces, attributable, and revocable
// one island at a time.
//
// That deliberately leaves existing islands as exposed as they were: the
// migration converts a silent default into a recorded grant, it does not decide
// for the operator. The Grandfathered marker is what makes the remaining
// exposure enumerable, and the surfaces treat it as an open to-do rather than a
// settled state.
//
// Reports whether anything changed (so Load persists only on a real migration).
func (p *Project) migrateHostGitHub() bool {
	if p.HostGitHubReviewed {
		return false
	}
	p.HostGitHubReviewed = true
	// Tenant islands already got no host credential — there is nothing to
	// preserve, and granting one here would hand them access they never had.
	if p.IsHostOwned() && p.HostGitHubGrant == nil {
		p.HostGitHubGrant = &HostGitHubGrant{
			GrantedAt:     time.Now().UTC(),
			Grandfathered: true,
		}
	}
	return true
}
