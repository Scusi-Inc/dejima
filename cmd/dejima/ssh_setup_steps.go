package main

import (
	"fmt"
	"strings"
)

// The SSH-façade setup steps, in one place so every surface that needs them —
// `agent open`, `ssh enroll`, `ssh config`, doctor, and the TUI gateway-UI
// nudge — prints the SAME two steps. Reaching any gateway agent's UI (OpenClaw,
// Letta, Goose) tunnels over the façade, which is opt-in, so every operator
// hits this once. The guidance used to be terse, scattered, and in one place
// outdated; this keeps it consistent and actionable.

// suggestedSSHAddr returns the address to prefill in the enable command. The
// host's own tailnet IP is exactly what a remote client dials, so prefer it;
// fall back to a placeholder when Tailscale isn't resolvable here.
func suggestedSSHAddr() string {
	if ip, ok := tailscaleIPv4(); ok {
		return ip + ":2222"
	}
	return "<tailnet-addr>:2222"
}

// enableFacadeCommand returns the exact command to enable the façade on the
// daemon host. Best-effort exact reconstruction: when we're ON the host (local
// socket) and can read the system LaunchDaemon's arguments, rebuild the operator's
// current `service install` invocation and append --ssh — so they never
// reassemble their flags. Otherwise a generic form with a "keep your flags" note.
//
// Returns the command and whether it was exactly reconstructed (vs generic).
func enableFacadeCommand() (cmd string, exact bool) {
	addr := suggestedSSHAddr()
	// Only attempt reconstruction when addressing the local daemon; a remote
	// client can't read the host's plist.
	if _, _, source := resolveTarget(); source == "local" {
		if args, err := systemDaemonArgs(); err == nil && len(args) > 0 {
			// args[0] is the dejimad binary path; the rest are its flags. Drop any
			// existing --ssh (and its value) so we don't duplicate it, then append
			// ours. Rebuild as a `dejima service install --system` command.
			flags := stripFlag(args[1:], "--ssh")
			parts := append([]string{"sudo dejima service install --system"}, flags...)
			parts = append(parts, "--ssh "+addr)
			return strings.Join(parts, " "), true
		}
	}
	return "sudo dejima service install --system --ssh " + addr + "   (keep your existing flags)", false
}

// stripFlag removes a "--flag value" pair (space- or =-joined) from a daemon
// arg list, so a reconstructed command doesn't carry a stale copy of a flag
// we're about to re-add.
func stripFlag(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == flag { // "--ssh" "value" (space-separated)
			i++ // skip the value too
			continue
		}
		if strings.HasPrefix(a, flag+"=") { // "--ssh=value"
			continue
		}
		out = append(out, a)
	}
	return out
}

// sshFacadeSetupSteps is the full, numbered guidance for the CLI. Shown wherever
// a command needs the façade and it isn't enabled.
func sshFacadeSetupSteps() string {
	enable, _ := enableFacadeCommand()
	return "gateway agents (OpenClaw, Letta, Goose) reach their UI over the daemon's SSH\n" +
		"façade, which isn't enabled yet. Two one-time steps:\n\n" +
		"  1. On the daemon HOST (needs sudo), enable it:\n" +
		"       " + enable + "\n\n" +
		"  2. On THIS device, authorize your key:\n" +
		"       dejima ssh enroll\n\n" +
		"Then `dejima agent open <island>` (or ⏎ on the agent in the dashboard) opens its UI."
}

// sshFacadeSetupStepsTUI is the condensed form for a TUI notice — the client
// step points at the in-TUI enroll (m → SSH setup) rather than the CLI.
func sshFacadeSetupStepsTUI() string {
	enable, _ := enableFacadeCommand()
	return fmt.Sprintf("gateway UI needs the SSH façade. 1) on the daemon host: %s   "+
		"2) here: press m → SSH setup to enroll this device", enable)
}
