package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// The agent harness has its own coordination channel, and Dejima does not sit
// on it.
//
// Every crossing Dejima advertises as gated — Port, capability, MCP, link,
// spawn, egress — is gated because the daemon is in the path. Claude Code's
// Remote Control is not in that path: it is an outbound WSS connection from the
// island to Anthropic, authenticated by the operator's own account, over which
// one session can enumerate and message every other session on that account.
// Verified from inside an island on 2026-09-13: `dejima link ls` correctly
// refused ("route not permitted for an island token") while the same container
// listed 57 peer sessions across unrelated machines and repositories.
//
// Two things follow, and only the second is actionable:
//
//   - Dejima cannot LEDGER that channel. There is no hook into the relay. Any
//     doc claiming every crossing is recorded has to name this exception.
//   - Dejima CAN GATE it, because the harness reads policy from a file, and a
//     file mounted read-only from the host is beyond the reach of the agent —
//     including an agent with root, since the island's capability bounding set
//     has no CAP_SYS_ADMIN and a read-only bind cannot be remounted rw.
//
// This file is the second one.
//
// What it is NOT: a fix for the launch flag. The first instinct was to make
// sure Dejima never passes `--remote-control`, and that is worthless — the
// island observed above was launched as plain `claude` and Remote Control was
// connected anyway, auto-enabled server-side. A convention about flags is not a
// gate, and in this case it was not even a convention that held.

// HarnessPolicyMountPath is where an island's harness policy dir is mounted.
//
// A DIRECTORY, not the file inside it, and for the reason secretsMountPath
// learned the hard way: a file bind binds the inode, so an operator editing the
// policy on the host with any editor that writes-and-renames (which is most of
// them) would leave the container reading the original inode forever — a
// containment setting that silently stops tracking what the operator set. The
// path is the harness's own managed-settings location; the file it reads is
// HarnessPolicyMountPath + "/" + harnessPolicyFileName.
const HarnessPolicyMountPath = "/etc/claude-code"

// harnessPolicyFileName is the harness's managed-settings filename. Managed
// settings outrank user and project settings, which is the property being
// bought here: the island's own ~/.claude/settings.json cannot loosen these,
// and neither can an agent that edits it.
const harnessPolicyFileName = "managed-settings.json"

// harnessPolicy is the subset of the harness's managed settings Dejima sets.
//
// Deliberately a struct with explicit fields rather than a map: these keys are
// a contract with a third-party binary, and the next person to add one should
// have to read why each existing one is here.
type harnessPolicy struct {
	// IsolatePeerMachines requires explicit human approval before the agent can
	// message a session on ANOTHER MACHINE over Remote Control. It does not
	// disable Remote Control, does not touch operator-to-agent steering, and
	// does not touch same-machine messaging between agents in this island.
	//
	// Two properties earn it the default slot. The harness marks it
	// bypassImmune, so `--dangerously-skip-permissions` cannot skip the prompt;
	// and it is not classifier-routed, so auto-mode cannot self-approve it. It
	// is one of the few harness settings an agent cannot talk its way past.
	//
	// The cost is real and worth stating: it is an APPROVAL, not a deny, so an
	// unattended island will stall on a cross-machine send rather than refuse
	// it, and the harness also prompts when it merely cannot resolve whether a
	// name is local (a fetch failure reads as ambiguity). Stalling on ambiguity
	// is the right failure direction here.
	IsolatePeerMachines bool `json:"isolatePeerMachines"`
}

// defaultHarnessPolicy is what a new island gets.
//
// Note what is NOT in it: crossSessionInbound. That setting ("accept" / "hold"
// / "refuse") is the INBOUND half, and inbound is the direction that actually
// carries risk — a message from another session is instruction-shaped input to
// something holding a shell. It belongs here on the merits, and it is absent
// because it was tested between two agents in one island and it refuses
// SAME-MACHINE peers too: the sender's refusal notice named the recipient as
// `uds:/tmp/cc-socks/57.sock`, the local socket. Defaulting it would cut
// in-island agent-to-agent messaging along with the traffic it is aimed at.
//
// Left to the operator per island rather than decided here, because whether
// that trade is right depends on whether an island's agents talk to each other.
// docs/harness-peer-isolation.md has the measurement and how to turn it on.
func defaultHarnessPolicy() harnessPolicy {
	return harnessPolicy{IsolatePeerMachines: true}
}

// islandHarnessPolicyDir materializes the island's harness policy file and
// returns the dir to mount read-only at HarnessPolicyMountPath.
//
// NON-CLOBBERING on purpose, and this is the one place the file departs from
// how the credential mounts behave. Those are re-derived every create because
// the daemon owns their content. This file the OPERATOR owns: editing it on the
// host is the supported way to opt an island out, or to add settings Dejima
// does not set. Rewriting it each create would make that edit last until the
// next `dejima upgrade` and then silently revert — a containment setting that
// changes under the operator is worse than one that is merely strict.
//
// The dir is returned even when the island runs no Claude Code agent, for the
// reason islandLLMConfigDir gives: mounting unconditionally means an agent
// added later reaches a container that already exists, instead of needing a
// recreate nobody knows to run.
func islandHarnessPolicyDir(p *project.Project) (string, error) {
	dir, err := paths.HarnessPolicyIslandDir(p.Name)
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, harnessPolicyFileName)
	if _, err := os.Stat(file); err == nil {
		return dir, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat harness policy: %w", err)
	}
	blob, err := json.MarshalIndent(defaultHarnessPolicy(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode harness policy: %w", err)
	}
	blob = append(blob, '\n')
	// Write-and-rename so a container starting concurrently never reads a torn
	// file. The harness treats an unparseable managed-settings.json as a reason
	// to warn and fall back, which on this file would mean falling back to no
	// isolation at all.
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return "", fmt.Errorf("write harness policy: %w", err)
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("install harness policy: %w", err)
	}
	return dir, nil
}
