package api

import (
	"context"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// Credential mounts are decided ONCE, at container create (credentialBindMounts
// runs only inside createContainerForProject). Everything that reports on them
// from the island's config is therefore answering a question about intent, at a
// moment when the operator is reading it as a question about the running
// island. Those two answers diverge the instant a grant changes and the
// container hasn't been recreated:
//
//	revoke, no recreate  → config says none, the credential is STILL mounted
//	grant,  no recreate  → config says granted, the island has NO such credential
//
// The first is the dangerous one — a containment surface under-reporting access
// is exactly the shape that reassures instead of failing. The second is merely
// wrong. Both are answered by asking the runtime what is mounted rather than
// inferring it from the record.
//
// The third state matters as much as the other two: when the container can't be
// inspected we must say so. "No mounts found" and "I couldn't look" are the same
// empty list and completely different facts, and rendering the second as the
// first manufactures a clean bill of health. Hence Known.

// credentialMount describes one credential the island can be given, in terms an
// operator reads rather than container paths.
type credentialMount struct {
	label string
	path  string
}

// credentialMounts enumerates every credential handed to an island as a mount.
// One list, so adding a credential means editing here rather than remembering
// each surface that reports on them — the same reason grantKinds exists in the
// TUI. gh is the widest, but `dejima secret rm` on a running island has exactly
// the same shape, and so will the next one.
// secretsMountPath is where an island's secrets DIRECTORY is bind-mounted. The
// file inside it is secretsMountPath + "/secrets.env".
//
// A directory, and a new path: the old mount put the FILE at
// /opt/host/secrets.env, which bound the inode and made every later set/remove
// invisible in the island (see island_secrets.go). Containers created before
// this still carry the old file mount, so they report drift here and need a
// recreate — which is exactly true, and is the only way they get the fix.
const secretsMountPath = "/opt/host/secrets.d"

// legacySecretsMountPath is where the secrets FILE used to be mounted, and is
// still mounted alongside the directory above.
//
// Not politeness — it closes a silent data-loss window during rollout.
// `dejima upgrade` recreates a container against WHATEVER image is already on
// the host; it does not rebuild one. So an operator who updates dejimad without
// re-running `make image` gets a new daemon mounting secrets.d and an old image
// whose load-secrets.sh only ever looks at secrets.env — and every secret in
// that island silently disappears. Mounting both means an old image keeps
// behaving exactly as it does today (stale, which is the bug, but not absent)
// while a new image prefers the directory and is correct. Removable once no
// island can still be running a pre-secrets.d image.
const legacySecretsMountPath = "/opt/host/secrets.env"

func credentialMounts() []credentialMount {
	return []credentialMount{
		{"GitHub credential", "/opt/host/gh-config"},
		{"secrets", secretsMountPath},
	}
}

// CredentialMountState is one credential's configured-vs-effective state.
type CredentialMountState struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	// Configured is what the island's config implies it should have.
	Configured bool `json:"configured"`
	// Mounted is what the container actually has right now.
	Mounted bool `json:"mounted"`
}

// Drifted reports whether intent and effect disagree — i.e. whether a container
// recreate is needed before the record describes reality.
func (c CredentialMountState) Drifted() bool { return c.Configured != c.Mounted }

// CredentialMountReport is the answer to "what credentials does this island
// actually have right now".
type CredentialMountReport struct {
	// Known is false when the runtime couldn't be asked — no container yet, or
	// the engine didn't answer. Consumers MUST render this as "not determined"
	// rather than as agreement; an unasked question is not a clean answer.
	Known bool `json:"known"`
	// Reason explains a false Known, for the surface to show verbatim.
	Reason string                 `json:"reason,omitempty"`
	States []CredentialMountState `json:"states,omitempty"`
}

// Drift returns the credentials whose configured and mounted states disagree.
// Always empty when !Known — we report ignorance as ignorance, never as
// agreement.
func (r CredentialMountReport) Drift() []CredentialMountState {
	if !r.Known {
		return nil
	}
	var out []CredentialMountState
	for _, s := range r.States {
		if s.Drifted() {
			out = append(out, s)
		}
	}
	return out
}

// credentialMountReport asks the runtime what is mounted and compares it with
// what the island's config says it should have.
func (s *Server) credentialMountReport(ctx context.Context, p *project.Project) CredentialMountReport {
	want, err := credentialBindMounts(p)
	if err != nil {
		return CredentialMountReport{Reason: "couldn't compute the island's configured mounts: " + err.Error()}
	}
	// "There is no container" and "I couldn't ask" both come back from
	// ContainerMounts as an error, and they are NOT the same fact — the first is
	// a determined answer (nothing is mounted, because there is nothing to mount
	// into), the second is ignorance. Collapsing them would make every
	// not-yet-created island report "not determined", which is the same
	// over-claiming-uncertainty mirror of the bug this file exists to fix. So
	// ask the status first.
	var have []string
	st, serr := s.rt.Status(ctx, p.ContainerName())
	missing := serr == nil && st == runtime.StatusMissing
	if !missing {
		var merr error
		have, merr = s.rt.ContainerMounts(ctx, p.ContainerName())
		if merr != nil {
			// A container exists (or its status is itself unknown) and the engine
			// didn't answer. We did not find out, and saying "nothing is mounted"
			// here would be a claim we haven't earned.
			return CredentialMountReport{Reason: "the island's container couldn't be inspected"}
		}
	}
	// A missing container falls through with have == nil: nothing is mounted,
	// which is a determined answer. Configured is still compared, so a grant
	// made before the island is created correctly shows as pending rather than
	// being silently zeroed.
	mounted := make(map[string]bool, len(have))
	for _, d := range have {
		mounted[d] = true
	}
	configured := make(map[string]bool, len(want))
	for _, b := range want {
		configured[b.ContainerPath] = true
	}
	rep := CredentialMountReport{Known: true}
	for _, cm := range credentialMounts() {
		rep.States = append(rep.States, CredentialMountState{
			Label:      cm.label,
			Path:       cm.path,
			Configured: configured[cm.path],
			Mounted:    mounted[cm.path],
		})
	}
	return rep
}
