package api

import (
	"sort"

	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/project"
)

// A GitHub identity listing that does not say WHICH ISLANDS USE EACH IDENTITY is
// a list you cannot act on.
//
// The state that caused an incident, printed by the old `dejima github ls`:
//
//	    NAME     LOGIN   HOST
//	*   github   aoos    github.com     <- default, freshly refreshed
//	    aoos     aoos    github.com     <- expired
//
// Every visible field agrees. Same login, same host, same shape. The `*` says
// which is the default and the operator refreshed the default, correctly, after
// which eight islands kept failing with "Bad credentials" — because all eight
// pinned `aoos` and NOTHING used the default. The one fact that would have ended
// it in ten seconds (`github` is used by zero islands) was on no surface at all;
// it lived in eight separate config.toml files.
//
// So usage is computed the way the DAEMON resolves it at materialization time,
// not by reading the raw pin: an island with a blank pin genuinely uses the
// default and must be counted against it. Attributing by pin would show the
// default with zero users on a host where every island legitimately follows it —
// the same wrong conclusion from the opposite direction.

// identityUsage returns, per identity NAME, the islands that resolve to it, plus
// the islands whose pin resolves to nothing at all.
//
// A DANGLING pin is its own diagnosis and must not be silently folded into "not
// used": the island names an identity the store does not have (deleted, renamed,
// or owned by another tenant), so it materializes NO credential and every git
// operation in it fails. That looks identical from inside the island to a token
// that has expired, and the fix is completely different.
func identityUsage(store *githubid.Store) (map[string][]string, []DanglingIdentityPin) {
	used := map[string][]string{}
	var dangling []DanglingIdentityPin

	projects, err := project.List()
	if err != nil {
		// Best-effort: a listing that cannot read projects still shows the
		// identities. Returning nil here means "unknown", and the renderer says
		// so rather than printing a confident zero.
		return nil, nil
	}
	for _, p := range projects {
		id, ok := store.ResolveForIsland(ghOwner(p.Owner), p.GitHubIdentity)
		if !ok {
			if p.GitHubIdentity != "" {
				dangling = append(dangling, DanglingIdentityPin{
					Island: p.Name, Identity: p.GitHubIdentity,
				})
			}
			continue
		}
		used[id.Name] = append(used[id.Name], p.Name)
	}
	for k := range used {
		sort.Strings(used[k])
	}
	sort.Slice(dangling, func(i, j int) bool { return dangling[i].Island < dangling[j].Island })
	return used, dangling
}

// identityViews decorates identity metadata with the islands resolving to each.
func identityViews(store *githubid.Store, metas []githubid.Meta) ([]GitHubIdentityView, []DanglingIdentityPin) {
	used, dangling := identityUsage(store)
	out := make([]GitHubIdentityView, 0, len(metas))
	for _, m := range metas {
		out = append(out, GitHubIdentityView{Meta: m, Islands: used[m.Name]})
	}
	return out, dangling
}
