package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/aoos/dejima/internal/wsl"
)

// The SSH-façade setup steps, in one place so every surface that needs them —
// `agent open`, `ssh enroll`, `ssh config`, doctor, and the TUI gateway-UI
// nudge — prints the SAME two steps. Reaching any gateway agent's UI (OpenClaw,
// Letta, Goose) tunnels over the façade, which is opt-in, so every operator
// hits this once. The guidance used to be terse, scattered, and in one place
// outdated; this keeps it consistent and actionable.

// sshAddrFromDaemonHost derives the façade bind address from the daemon host we
// are connected to, or "" when it cannot (local socket, wsl://, malformed).
//
// The port is replaced, not reused: 7273 is the API listener, 2222 the façade.
func sshAddrFromDaemonHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || wsl.IsHost(host) {
		return "" // no address to speak of; the local paths below apply
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil || strings.TrimSpace(h) == "" {
		return ""
	}
	return net.JoinHostPort(strings.TrimSpace(h), "2222")
}

// suggestedSSHAddr returns the address to prefill in the enable command.
//
// THE ADDRESS BELONGS TO THE DAEMON HOST, WHICH MAY NOT BE THIS MACHINE. The
// command it lands in is run with sudo ON THE HOST, and the address is what the
// façade BINDS. Asking the local `tailscale ip -4` answers for whatever device
// is typing — correct when that is the host, and wrong in a way that cannot
// work when it is not.
//
// It was reached the wrong way: a Mac driving a Mac mini over the tailnet got
// setup steps telling it to bind the MINI's façade to the LAPTOP's tailnet IP.
// Same shape as the `ollama serve` advice — a command for one machine, composed
// from facts about another.
//
// When the daemon is remote we already know its address: it is the host we are
// talking to. Prefer that over any local probe, and keep the local tailnet IP
// only for the case it was always right for — being on the host.
func suggestedSSHAddr() string {
	if host, _, source := resolveTarget(); source != "local" {
		if addr := sshAddrFromDaemonHost(host); addr != "" {
			return addr
		}
	}
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
//
// KEEP IT SHORT. The footer truncates, and this is the one-line form; the full
// guidance is sshFacadeHelpPane.
func sshFacadeSetupStepsTUI() string {
	enable, _ := enableFacadeCommand()
	return fmt.Sprintf("gateway UI needs the SSH façade. 1) on the daemon host: %s   "+
		"2) here: press m → SSH setup to enroll this device", enable)
}

// sshFacadeHelpPane is the full-pane guidance shown when an operator tries to
// open a gateway UI and the façade is off.
//
// IT IS A PANE BECAUSE THE FOOTER TRUNCATES AT 60 CHARACTERS. The condensed
// form above is ~180, so pressing ⏎ on an OpenClaw agent produced
//
//	⚠ gateway UI needs the SSH façade. 1) on the daemon host: su
//
// cut off mid-command. There was, in practice, no guidance at all — the
// operator saw a red bar naming a prerequisite and nothing about what to do.
//
// THE TWO STEPS ARE ON TWO DIFFERENT MACHINES, and that is the thing this has to
// get across. Step 1 runs on the daemon host, which for a laptop driving a Mac
// mini is not the machine reading the message.
//
// STEP 2 DOES NOT EXIST YET WHEN THIS IS SHOWN. The "SSH setup" action is gated
// on overview.SSHAddr — the menu builds it only once the daemon reports a
// façade — so an operator who goes looking for it before finishing step 1 finds
// nothing and reasonably concludes the instructions are wrong. Saying so is the
// difference between a sequence and a dead end.
// STYLE ONE LINE AT A TIME, never a string containing "\n". lipgloss pads a
// rendered block to its widest line and carries that padding across the
// newlines inside it, so a single multi-line Render indents everything written
// after it. The first draft did exactly that and produced a staircase — caught
// by printing the pane, not by the assertions, which were all still green.
func sshFacadeHelpPane(daemonLabel string, remote bool, enableCmd string, exact bool) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	muted := func(s string) { line(styleMuted.Render(s)) }

	line(styleTitle.Render("Gateway UI needs the SSH façade"))
	line("")
	muted("OpenClaw, Letta and Goose serve a web console from inside the island.")
	muted("Dejima reaches it by tunnelling over the daemon's SSH façade, which")
	muted("is off unless dejimad was started with --ssh.")
	line("")
	// Only claim two machines when there are two. On a local daemon both steps
	// are here, and saying otherwise sends the reader looking for another box.
	if remote {
		line("Two one-time steps, on two different machines.")
	} else {
		line("Two one-time steps, both on this machine.")
	}
	line("")

	if remote {
		line(styleAccent.Render(" 1  ") + "On the " + styleWaiting.Render("DAEMON HOST") +
			" — the machine running dejimad")
		line("    (" + daemonLabel + "), " + styleWaiting.Render("not this one") + ". In a terminal there:")
	} else {
		line(styleAccent.Render(" 1  ") + "On the " + styleWaiting.Render("DAEMON HOST") +
			" — this machine. In a terminal there:")
	}
	line("")
	line("      " + styleAccent.Render(enableCmd))
	if !exact {
		muted("      (keep any flags you already pass to dejimad)")
	}
	muted("      This restarts the daemon; islands keep running.")
	line("")

	line(styleAccent.Render(" 2  ") + "Back " + styleWaiting.Render("HERE") +
		", after step 1, once this dashboard reconnects:")
	line("")
	line("      press " + styleAccent.Render("m") + " → " + styleAccent.Render(`"SSH setup"`) +
		" to authorize this device")
	// The honest caveat. Without it the obvious move is to go press m NOW, find
	// nothing, and conclude the instructions are wrong.
	muted("      That entry appears only after step 1 — the menu builds it from")
	muted("      the façade address the daemon reports, so it is genuinely not")
	muted("      there yet. This is a sequence, not a missing button.")
	line("")
	line("Then press " + styleAccent.Render("⏎") + " on the agent again to open its console.")
	line("")
	muted("esc to close")
	return b.String()
}
