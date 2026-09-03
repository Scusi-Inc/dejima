package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// gitHubCredentialFrom decides whether `dejima secret set GH_TOKEN` warns about
// overriding the island's GitHub credential, and — the part that matters — how
// it behaves when it CANNOT tell.
//
// The not-found branch used to return known=true, i.e. "I looked, and this
// island has no GitHub credential". That is an assertion the function never
// earned: no entry matching the path means the daemon's mount table and this
// caller have drifted apart, which is ignorance, not absence. And it is not a
// harmless mislabel, because warnGitHubTokenPrecedence returns early on
// known-and-absent and prints NOTHING — not even its conditional form. So a
// renamed mount path would take the warning offline silently, on the one secret
// name that can change which identity every clone and push uses.
//
// This branch is unreachable through a live daemon: credentialMounts() always
// enumerates the gh entry, so a real report carries it with Configured and
// Mounted false. It only runs once the two sides disagree — which is precisely
// when nothing else is watching.
func TestGitHubCredentialUnknownWhenTheMountIsNotInTheReport(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rep        api.CredentialMountReport
		wantHas    bool
		wantKnown  bool
		wantReason string
	}{
		{
			name: "path drifted — no entry matches",
			rep: api.CredentialMountReport{Known: true, States: []api.CredentialMountState{
				{Label: "GitHub credential", Path: "/opt/host/gh-cfg", Configured: true, Mounted: true},
				{Label: "secrets", Path: "/opt/host/secrets.d", Mounted: true},
			}},
			wantHas: false, wantKnown: false,
			wantReason: "an unrecognised mount table is not evidence of absence",
		},
		{
			name:    "empty report that still claims to be known",
			rep:     api.CredentialMountReport{Known: true},
			wantHas: false, wantKnown: false,
			wantReason: "no states at all cannot mean the credential is absent",
		},
		{
			name: "the runtime could not be asked",
			rep: api.CredentialMountReport{Known: false, States: []api.CredentialMountState{
				{Label: "GitHub credential", Path: api.GitHubCredentialMountPath, Mounted: true},
			}},
			wantHas: false, wantKnown: false,
			wantReason: "!Known must win over whatever the stale states say",
		},
		{
			name: "granted but pending a recreate still counts",
			rep: api.CredentialMountReport{Known: true, States: []api.CredentialMountState{
				{Label: "GitHub credential", Path: api.GitHubCredentialMountPath, Configured: true, Mounted: false},
			}},
			wantHas: true, wantKnown: true,
			wantReason: "warning after the recreate would be exactly one recreate too late",
		},
		{
			name: "mounted but no longer configured still counts",
			rep: api.CredentialMountReport{Known: true, States: []api.CredentialMountState{
				{Label: "GitHub credential", Path: api.GitHubCredentialMountPath, Configured: false, Mounted: true},
			}},
			wantHas: true, wantKnown: true,
			wantReason: "the container has it right now, so GH_TOKEN would override it right now",
		},
		{
			name: "genuinely absent",
			rep: api.CredentialMountReport{Known: true, States: []api.CredentialMountState{
				{Label: "GitHub credential", Path: api.GitHubCredentialMountPath},
			}},
			wantHas: false, wantKnown: true,
			wantReason: "the entry is present and says no — that IS a determined answer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			has, known := gitHubCredentialFrom(tc.rep)
			if has != tc.wantHas || known != tc.wantKnown {
				t.Errorf("got (has=%v, known=%v), want (has=%v, known=%v) — %s",
					has, known, tc.wantHas, tc.wantKnown, tc.wantReason)
			}
		})
	}
}

// The path is the daemon's, not a copy. A local literal drifts silently, and it
// drifts toward silence rather than toward a wrong answer, which is worse.
func TestGitHubCredentialMountPathComesFromTheDaemon(t *testing.T) {
	rep := api.CredentialMountReport{Known: true, States: []api.CredentialMountState{
		{Label: "GitHub credential", Path: api.GitHubCredentialMountPath, Mounted: true},
	}}
	if has, known := gitHubCredentialFrom(rep); !has || !known {
		t.Errorf("the canonical mount path must match; got (has=%v, known=%v). "+
			"if this fails, cmd/dejima is matching on something other than api.GitHubCredentialMountPath", has, known)
	}
}
