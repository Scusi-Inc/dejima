package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/invite"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/vmmem"
	"github.com/aoos/dejima/internal/wsl"
)

// newOnboardCmd is the explicit re-entry into the wizard. Always runs,
// regardless of whether the dismissal marker exists.
func newOnboardCmd() *cobra.Command {
	var provisionHost, yes, reset, newHost bool
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Walk through Dejima setup (run anytime to (re)configure).",
		Long: "Interactive wizard. Detects what's already on this machine, asks what " +
			"you're trying to do, and prints a tailored set of commands. Safe to run " +
			"more than once.\n\n" +
			"`--provision-host` (macOS) runs the full host-provisioning wizard: a fresh " +
			"Mac mini → working Dejima agent server in one command (never-sleep power " +
			"settings, Homebrew/Tailscale/Docker, then the daemon). Resumable; --yes runs " +
			"non-interactively; --reset starts the provisioning from scratch.\n\n" +
			"`--new-host` guides setting up a SEPARATE fresh Mac mini as a host from this " +
			"machine: the pre-SSH steps (account, Remote Login, the headless Tailscale " +
			"auth-key route) and the handoff to `--provision-host`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if newHost {
				// The zero-to-host guide sets up a DIFFERENT machine, not this one,
				// so there's no local daemon to health-check — just record that
				// onboarding was seen.
				if err := runNewHostGuide(cmd.Context()); err != nil {
					return err
				}
				_ = writeDismissalMarker()
				return nil
			}
			if provisionHost {
				if err := runProvisionHost(cmd.Context(), yes, reset); err != nil {
					return err
				}
				if !markSetupDoneIfHealthy(cmd.Context()) {
					return errSetupIncomplete
				}
				return nil
			}
			if err := runOnboarding(cmd.Context()); err != nil {
				return err
			}
			// Explicit `dejima onboard` dismisses the first-run prompt only if the
			// daemon is actually reachable — otherwise we'd cache a half-setup the
			// user can never re-trigger guided help for.
			if !markSetupDoneIfHealthy(cmd.Context()) {
				return errSetupIncomplete
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&provisionHost, "provision-host", false, "run the macOS host-provisioning wizard (fresh Mac mini → Dejima agent server)")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --provision-host: auto-confirm scriptable steps, skip GUI pauses (collect them into a checklist)")
	cmd.Flags().BoolVar(&reset, "reset", false, "with --provision-host: clear saved progress and start the provisioning from scratch")
	cmd.Flags().BoolVar(&newHost, "new-host", false, "guide setting up a SEPARATE fresh Mac mini as a host (the pre-SSH steps + handoff)")
	return cmd
}

// ---------------------------------------------------------------------------
// First-run prompt — yes / not now / never
// ---------------------------------------------------------------------------

// firstRunPrompt is called by the root `dejima` command (no args) when the
// dismissal marker is absent. Returns (continueToTUI, err).
//
// Every choice ends in the TUI — declining the wizard means "skip setup," not
// "abandon Dejima." The choices differ only in what happens first and whether
// we ask again. (If nothing is configured yet, the TUI surfaces a
// daemon-unreachable state rather than failing.)
//
//   - "yes"     → runs the wizard, writes the marker, then opens the TUI. The
//     wizard's printed steps are restored on screen when the TUI
//     (alt-screen) exits.
//   - "not now" → opens the TUI; marker NOT written, so the prompt reappears
//     on the next run.
//   - "never"   → writes the marker, then opens the TUI; never prompts again.
//   - non-TTY   → opens the TUI (which handles the unconfigured state).
func firstRunPrompt(ctx context.Context) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return true, nil // can't prompt without a TTY; defer to TUI which handles unconfigured state.
	}

	// Adapt the question to what this machine looks like, so the first prompt
	// matches the user's actual situation instead of asking everyone the same
	// generic thing. A cheap probe (no docker/tailscale shell-outs) keeps the
	// no-args path snappy.
	kind := detectFirstRunContext(ctx)
	switch kind {
	case firstRunConfigured:
		// Already talking to a daemon — nothing to set up. Drop straight into the
		// dashboard and stop nagging.
		_ = writeDismissalMarker()
		return true, nil

	case firstRunClientUnreachable:
		host, label, source := resolveTarget()
		fmt.Println()
		fmt.Println(bold("Can't reach your Dejima host"))
		fmt.Println()
		switch source {
		case "profile":
			// The just-joined-teammate case: a saved active profile, host down now.
			fmt.Printf("  Your active profile %q points at %s, which isn't answering right now.\n", label, host)
		case "flag":
			fmt.Printf("  The host you launched with (%s) isn't answering.\n", host)
		case "env":
			fmt.Printf("  DEJIMA_HOST is set (%s) but the daemon isn't answering.\n", host)
		default:
			fmt.Println("  Your configured Dejima host isn't answering right now.")
		}
		fmt.Println()
		if ans := readSingleKey("Troubleshoot the connection now? [Y/n]: "); ans == "" || strings.EqualFold(ans, "y") {
			runConnectionTroubleshooter(ctx)
		}
		fmt.Println("Opening the dashboard. Re-run `dejima onboard` anytime.")
		return true, nil
	}

	// No daemon reachable and no host target (firstRunFreshHost or
	// firstRunGeneric). The ONE question that routes everything: set up a server
	// here, or join one that already exists? Offering "join" closes the gap that
	// stranded teammates (#68) — the prompt used to offer only "set up", which on
	// a shared host spawns a daemon that COLLIDES with the operator's already
	// running on :7273/:7274. Route first; then ask only what the chosen branch
	// needs (join → paste an invite; set up → proceed).
	fmt.Println()
	fmt.Println(bold("First time — set up Dejima on this machine, or join one that already exists?"))
	fmt.Println()
	if kind == firstRunWindowsClient {
		// Be honest about the shape of "here" on Windows: the daemon can't run on
		// Windows itself, so "set up here" means WSL2. Saying that up front beats
		// letting the user pick "s" and then discovering the constraint.
		fmt.Println("    s) Set up Dejima on this machine — in WSL2 (Windows can't run the daemon directly)")
	} else {
		fmt.Println("    s) Set up Dejima on this machine (run a daemon here)")
	}
	fmt.Println("    j) Join an existing server — paste an invite from your team")
	fmt.Println("    n) Not now — ask me again next time")
	fmt.Println("    N) Never ask again")
	fmt.Println()
	switch readSingleKey("Choice [s/j/n/N]: ") {
	case "j", "J", "join":
		return firstRunJoin(ctx)
	case "N", "never":
		fmt.Println("Got it. Re-engage anytime with `dejima onboard`.")
		_ = writeDismissalMarker()
		return true, nil
	case "n", "no", "later", "":
		fmt.Println("OK — opening the dashboard. Run `dejima onboard` anytime.")
		return true, nil
	case "s", "S", "set", "setup", "y", "Y", "yes":
		// fall through to the set-up branch below
	default:
		// An unrecognized key is non-committal: open the dashboard rather than
		// guess a destructive default (installing a daemon on a shared host is the
		// exact #68 hazard). The prompt returns next run.
		fmt.Println("Didn't catch that — opening the dashboard. Re-run `dejima onboard` anytime.")
		return true, nil
	}

	// Set-up branch, dispatched to the context-specific flow: a fresh Mac gets the
	// richer host-provisioning sub-choice; anything else gets the generic
	// walkthrough.
	if kind == firstRunFreshHost {
		return firstRunSetUpHost(ctx)
	}
	if kind == firstRunWindowsClient {
		return firstRunSetUpWSL(ctx)
	}
	if err := runOnboarding(ctx); err != nil {
		return false, err
	}
	markSetupDoneIfHealthy(ctx) // dismiss only if dejimad is actually up
	return true, nil
}

// firstRunSetUpWSL is the "set up here" branch on Windows. dejimad needs a Unix
// host with Docker, which Windows isn't — but WSL2 is one, on this same machine,
// so "locally" is achievable after all. The client then reaches the daemon by
// tunnelling its Unix socket through wsl.exe, leaving the daemon's trust model
// untouched (no TCP listener, no token).
//
// Reached only after the router's "set up" choice, so it doesn't re-ask the
// set-up-vs-join question — it explains the WSL2 requirement and offers to do it.
func firstRunSetUpWSL(ctx context.Context) (bool, error) {
	fmt.Println()
	fmt.Println("  Dejima's daemon needs Linux + Docker, so on Windows it runs inside WSL2 —")
	fmt.Println("  still local, still your hardware. Setup installs Docker and dejimad into a")
	fmt.Printf("  WSL2 distro (%q) and connects this client to it.\n", wsl.DefaultDistro)
	fmt.Println()
	if !wsl.Available() {
		// WSL itself is missing; `wsl --install` needs admin AND a reboot, so we
		// can't just do it. Give the exact command rather than a wizard that
		// would fail two steps in.
		fmt.Println("  WSL isn't installed yet. In an " + bold("administrator") + " PowerShell:")
		fmt.Println()
		fmt.Println("      wsl --install")
		fmt.Println()
		fmt.Println("  Reboot, then run:  dejima wsl setup")
		fmt.Println()
		fmt.Println("  Prefer a server instead? `dejima join <invite>`, or")
		fmt.Println("  `dejima profile add <name> <host>:7273`.")
		fmt.Println()
		fmt.Println("Opening the dashboard.")
		return true, nil
	}
	fmt.Println("    y) Yes, set up the WSL2 host now (dejima wsl setup)")
	fmt.Println("    n) Not now")
	fmt.Println()
	switch readSingleKey("Choice [y/n]: ") {
	case "y", "Y", "yes", "":
		if err := runWSLSetup(ctx, "", false); err != nil {
			// A failed provision shouldn't strand the user at an error with no way
			// forward — the dashboard still works against any other target.
			fmt.Fprintf(os.Stderr, "\nWSL setup didn't finish: %v\n", err)
			fmt.Println("Re-run `dejima wsl setup` after fixing that, or point at a server with `dejima join <invite>`.")
			return true, nil
		}
		markSetupDoneIfHealthy(ctx)
		return true, nil
	default:
		fmt.Println("OK — opening the dashboard. Run `dejima wsl setup` anytime.")
		return true, nil
	}
}

// firstRunSetUpHost is the "set up here" branch on a fresh Mac: offer full host
// provisioning (power settings + Homebrew/Tailscale/Docker + daemon) or just the
// generic walkthrough. Reached only after the router's "set up" choice, so it no
// longer re-asks the set-up-vs-join question.
func firstRunSetUpHost(ctx context.Context) (bool, error) {
	fmt.Println()
	fmt.Println("  Provision this Mac into a secure, always-on Dejima host? That sets never-sleep")
	fmt.Println("  power settings, Homebrew/Tailscale/Docker, and the daemon — one walkthrough.")
	fmt.Println()
	fmt.Println("    y) Yes, provision this host (dejima onboard --provision-host)")
	fmt.Println("    g) Just the generic setup walkthrough")
	fmt.Println("    n) Not now")
	fmt.Println()
	switch readSingleKey("Choice [y/g/n]: ") {
	case "y", "Y", "yes":
		if err := runProvisionHost(ctx, false, false); err != nil {
			return false, err
		}
		markSetupDoneIfHealthy(ctx) // dismiss only if dejimad is actually up; else the prompt returns next run
		return true, nil
	case "g", "G":
		if err := runOnboarding(ctx); err != nil {
			return false, err
		}
		markSetupDoneIfHealthy(ctx)
		return true, nil
	default:
		fmt.Println("OK — opening the dashboard. Run `dejima onboard --provision-host` anytime.")
		return true, nil
	}
}

// joinFromInvite decodes a pasted invite blob and persists it as the active
// connection profile (host + token). It's the pure core of the join flow — no
// stdin, no network — shared with the CLI `dejima join` and the TUI switcher,
// and the seam first-run-join tests drive. Returns the decoded payload and the
// saved profile name.
func joinFromInvite(blob string) (invite.Payload, string, error) {
	p, err := invite.Decode(blob)
	if err != nil {
		return invite.Payload{}, "", err
	}
	name, err := clientcfg.SaveInvite(p)
	if err != nil {
		return p, "", err
	}
	return p, name, nil
}

// firstRunJoin is the teammate "Join a server" branch of the router: paste a
// `dejima-invite:` blob, persist it as the active connection profile (host +
// token), and connect — the no-CLI, no-env path a teammate needs to join an
// existing daemon (#68). It mirrors `dejima join <blob>` and the TUI switcher's
// join step, all three sharing invite.Decode + clientcfg.SaveInvite.
func firstRunJoin(ctx context.Context) (bool, error) {
	fmt.Println()
	fmt.Println(bold("Join an existing Dejima server"))
	fmt.Println()
	fmt.Println("  Paste the invite a teammate sent you (it starts with `dejima-invite:`).")
	fmt.Println("  It carries the daemon host + your access token — no env vars to set.")
	fmt.Println()
	blob := readSingleKey("Invite: ")
	if strings.TrimSpace(blob) == "" {
		fmt.Println("No invite entered — opening the dashboard. Run `dejima join <invite>` (or re-run `dejima onboard`) anytime.")
		return true, nil
	}
	p, name, err := joinFromInvite(blob)
	if err != nil {
		// Decode/save errors are user-facing strings (a1's contract) — show
		// verbatim, then fall through to the dashboard rather than blocking.
		fmt.Println(err)
		fmt.Println("Opening the dashboard. Try `dejima join <invite>` once you have a valid invite.")
		return true, nil
	}
	scope := "all islands"
	if len(p.Islands) > 0 {
		scope = strings.Join(p.Islands, ", ")
	}
	fmt.Printf("Joined %s as %s (scope: %s) — saved as profile %q and made active.\n", p.Host, p.Role, scope, name)
	// Confirm the connection now (bounded) so the teammate gets immediate
	// feedback, but never block the dashboard on it: the profile is saved either
	// way, and a transient failure shouldn't strand them. Probe the invite's own
	// host directly rather than via resolveHost/Health, which can resolve a
	// different target and mask an unreachable tailnet host as "verified".
	if !tcpReachable(p.Host) {
		if isTailscaleHost(p.Host) {
			// A Tailscale-pinned daemon is unreachable until the teammate is on the
			// tailnet — guide them there instead of the opaque timeout error.
			printTailscaleJoinHelp(p.Host)
		} else {
			fmt.Printf("note: couldn't reach %s yet — the profile is saved; the dashboard will retry.\n", p.Host)
		}
	} else {
		fmt.Println("Connection verified. Opening the dashboard.")
	}
	_ = writeDismissalMarker() // configured now — don't nag on the next run
	return true, nil
}

// firstRunContext is the situation the no-args first-run prompt adapts to.
type firstRunContext int

const (
	firstRunGeneric           firstRunContext = iota // can't tell — generic walkthrough
	firstRunConfigured                               // a daemon is already reachable
	firstRunClientUnreachable                        // DEJIMA_HOST set but daemon down → troubleshoot
	firstRunFreshHost                                // macOS, no daemon, looks like a host to provision
	firstRunWindowsClient                            // Windows: can't host dejimad here; WSL2 or a server
)

// detectFirstRunContext does a cheap classification of this machine. It avoids
// the heavier docker/tailscale probes in detectEnv() so the no-args path stays
// fast; the only network call is a short daemon health check.
func detectFirstRunContext(ctx context.Context) firstRunContext {
	reachable := false
	if c, err := api_client(); err == nil {
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if c.Health(hctx) == nil {
			reachable = true
		}
	}
	if reachable {
		return firstRunConfigured
	}
	// Not reachable — but if we HAVE a connection target (an explicit DEJIMA_HOST
	// or `-p` flag, OR a saved active profile from `dejima join <invite>` / the TUI
	// switcher), this machine is a CLIENT whose host is momentarily down, NOT a
	// fresh host to provision. resolveTarget() consults the profile, so a
	// just-joined teammate (no DEJIMA_HOST, no local daemon) lands on the
	// troubleshoot-then-dashboard path instead of the "set up a server" question
	// they were wrongly getting (#209).
	if _, _, source := resolveTarget(); source != "local" {
		return firstRunClientUnreachable
	}
	// No reachable daemon and no connection target at all. On Windows that's a
	// distinct situation from every Unix one: this machine CAN'T host dejimad, so
	// the "set up a daemon here" branch would be a dead end. The local answer is
	// a daemon in WSL2 — see firstRunSetUpWSL.
	if runtime.GOOS == "windows" {
		return firstRunWindowsClient
	}
	// On macOS with no daemon installed, this is the fresh-Mac-mini-host case the
	// provisioning wizard targets.
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("dejimad"); err != nil {
			return firstRunFreshHost
		}
	}
	return firstRunGeneric
}

// stdinReader is a single shared buffered reader over stdin. It must be shared
// across all prompts: bufio reads ahead, so a fresh reader per call would
// discard any input already buffered (breaking type-ahead, paste, and pipes).
var stdinReader = bufio.NewReader(os.Stdin)

// readSingleKey prompts and reads a line of input from stdin.
func readSingleKey(prompt string) string {
	fmt.Print(prompt)
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}

// ---------------------------------------------------------------------------
// Marker file
// ---------------------------------------------------------------------------

func dismissalMarkerPath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "onboarding-dismissed"), nil
}

func onboardingDismissed() bool {
	p, err := dismissalMarkerPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func writeDismissalMarker() error {
	p, err := dismissalMarkerPath()
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte("dismissed\n"), 0o600)
}

// errSetupIncomplete is returned by the explicit `dejima onboard` command when
// the wizard finished but dejimad still isn't reachable — a non-zero exit so a
// scripted `--provision-host --yes` / CI run sees the failure. The actionable
// detail is printed by markSetupDoneIfHealthy first; this is just the footer.
var errSetupIncomplete = errors.New("setup incomplete — dejimad is not reachable (see the steps above)")

// daemonHealthy reports whether the daemon for the *current target* answers a
// health check quickly. This is the single source of truth for "did setup
// actually work?" — distinct from "the wizard printed all its steps."
func daemonHealthy(ctx context.Context) bool {
	c, err := client()
	if err != nil {
		return false
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.Health(hctx) == nil
}

// markSetupDoneIfHealthy is the honest end of every setup flow. It records the
// run as done (writes the first-run dismissal marker) ONLY if dejimad is now
// reachable. When it isn't, it leaves the marker UNWRITTEN — so the first-run
// prompt returns next time rather than stranding the user on a cached half-setup
// (the exact failure that bit the dejimaqa box) — and prints the concrete next
// step. Returns whether setup is verified working.
func markSetupDoneIfHealthy(ctx context.Context) bool {
	if daemonHealthy(ctx) {
		_ = writeDismissalMarker()
		fmt.Println()
		fmt.Println(bold("✅ Setup verified — dejimad is reachable."))
		return true
	}
	if resolveHost() == "" {
		// Local host: reuse the daemon-unreachable diagnosis, framed as "setup
		// isn't finished" (probes the socket + service manager for the real cause).
		reportSetupIncomplete()
	} else {
		host := resolveHost()
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, bold("Setup isn't finished — can't reach "+host+" yet."))
		fmt.Fprintln(os.Stderr, "  Verify Tailscale is up here and the host exposes TCP, then re-run `dejima onboard`:")
		fmt.Fprintln(os.Stderr, "    • on the HOST:  dejima service install --tcp :7273")
		fmt.Fprintln(os.Stderr, "    • diagnose:     dejima doctor")
	}
	return false
}

// ---------------------------------------------------------------------------
// The wizard
// ---------------------------------------------------------------------------

func runOnboarding(ctx context.Context) error {
	env := detectEnv()

	fmt.Println()
	fmt.Println(bold("Dejima onboarding"))
	fmt.Println()
	printEnvSummary(env)
	fmt.Println()

	fmt.Println("What are you trying to do?")
	fmt.Println("  1) Set up a Dejima host (server side) on this machine")
	fmt.Println("  2) Connect this machine as a client to an existing Dejima host")
	fmt.Println("  3) Both — server here AND use the CLI from here too")
	fmt.Println("  4) Just exploring — show me the install options")
	fmt.Println("  5) Make this host reachable from other devices (Tailscale SSH + remote Dejima)")
	fmt.Println("  6) Set up a SEPARATE fresh Mac mini as a host (from here — I don't have one yet)")
	fmt.Println()

	switch readSingleKey("Choice [1/2/3/4/5/6]: ") {
	case "1":
		return printServerInstall(ctx, env, false)
	case "2":
		return printClientInstall(ctx, env)
	case "3":
		return printServerInstall(ctx, env, true)
	case "4":
		return printOverview()
	case "5":
		return setupRemoteAccess(ctx, env)
	case "6":
		return runNewHostGuide(ctx)
	default:
		fmt.Println("No choice made. Re-run anytime with `dejima onboard`.")
		return nil
	}
}

// runNewHostGuide walks turning a SEPARATE fresh Mac mini into a Dejima host
// from THIS machine — the "zero-to-host" steps that happen before
// `onboard --provision-host` can even be reached: first boot + an account,
// Remote Login, getting on the network (notably the headless Tailscale auth-key
// route, since a headless mini has no browser to log in), then the handoff.
//
// The pre-SSH bits are inherently manual (the macOS Setup Assistant is GUI-only
// on first boot); from SSH-reachable onward it hands off to --provision-host.
// Driving that remote provision over SSH from here is a planned follow-up; for
// now we print the exact remote commands (personalized if a host is given).
func runNewHostGuide(ctx context.Context) error {
	fmt.Println()
	fmt.Println(bold("Set up a fresh Mac mini as a Dejima host (from this machine)"))
	fmt.Println()
	fmt.Println("A Mac mini's first boot is GUI-only, so the first steps are hands-on; once")
	fmt.Println("you can SSH in, `dejima onboard --provision-host` does the rest.")
	fmt.Println()

	fmt.Println(bold("1. First boot") + " (on the mini, with a monitor + keyboard for setup)")
	fmt.Println("   • Power on, complete the macOS Setup Assistant.")
	fmt.Println("   • Create an admin account and NOTE its shortname + password — you SSH in as it.")
	fmt.Println()

	fmt.Println(bold("2. Enable Remote Login (SSH)") + " on the mini")
	fmt.Println("   • System Settings → General → Sharing → Remote Login → ON")
	fmt.Println("   • (or in a terminal on the mini: `sudo systemsetup -setremotelogin on`)")
	fmt.Println()

	fmt.Println(bold("3. Get the mini on your network"))
	fmt.Println("   Recommended — Tailscale (reachable anywhere, no port-forwarding):")
	fmt.Println("     • Install: `brew install --cask tailscale-app` (or https://tailscale.com/download)")
	fmt.Println("     • HEADLESS mini (no browser to log in)? Use a pre-auth key — generate one at")
	fmt.Println("       https://login.tailscale.com/admin/settings/keys, then on the mini:")
	fmt.Println("         `sudo tailscale up --ssh --auth-key=tskey-auth-xxxxx`")
	fmt.Println("       Its name becomes <hostname>.<tailnet>.ts.net.")
	fmt.Println("   Or LAN only:")
	fmt.Println("     • Find its IP on the mini: `ipconfig getifaddr en0` (Wi-Fi: `en1`)")
	fmt.Println()

	// Personalize the rest if the user can name the host + account now.
	host := strings.TrimSpace(readSingleKey("The mini's Tailscale name or IP (blank to skip): "))
	user := ""
	if host != "" {
		user = strings.TrimSpace(readSingleKey("The admin account shortname on the mini (blank to skip): "))
	}
	fmt.Println()

	sshTarget := "<admin>@<mini-name-or-ip>"
	if host != "" {
		if user != "" {
			sshTarget = user + "@" + host
		} else {
			sshTarget = host
		}
	}

	fmt.Println(bold("4. Confirm you can reach it from here"))
	fmt.Printf("   • `ssh %s`  (accept the host key on first connect)\n", sshTarget)
	if host != "" {
		if probeSSH(ctx, host) {
			fmt.Println("   ✓ port 22 is open on the mini")
		} else {
			fmt.Println("   (couldn't confirm port 22 yet — finish steps 2–3 above, then retry)")
		}
	}
	fmt.Println()

	fmt.Println(bold("5. Install Dejima on the mini and provision it"))
	fmt.Println("   On the mini (over SSH or at its keyboard):")
	fmt.Println("     curl -fsSL https://dejima.tech/install.sh | bash")
	fmt.Println("     dejima onboard --provision-host")
	fmt.Println("   That handles never-sleep power settings, Homebrew/Docker/Tailscale, and")
	fmt.Println("   installs the daemon as a boot service.")
	fmt.Println()

	fmt.Println(bold("6. Back here — point your CLI at it"))
	if host != "" {
		fmt.Printf("   export DEJIMA_HOST=%s:7273 && dejima ls\n", host)
	} else {
		fmt.Println("   export DEJIMA_HOST=<mini-name-or-ip>:7273 && dejima ls")
	}
	fmt.Println()
	fmt.Println("(Coming soon: running step 5 for you over SSH from this machine.)")
	return nil
}

// sshProbeHost normalizes a user-entered host to a bare host for probeSSH:
// strips a trailing :port if one was appended (net.SplitHostPort), and leaves a
// bare IPv6 literal or a plain name/IP untouched. Pure, so it's unit-tested in
// place of the inherently environment-dependent network dial.
func sshProbeHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h // had a :port (or bracketed IPv6 + port) → bare host
	}
	return host // no port — a plain name/IP or bare IPv6 like ::1
}

// probeSSH best-effort reports whether port 22 is open on host, to give the
// new-host guide immediate "yes you can reach it" feedback. Never fatal.
func probeSSH(ctx context.Context, host string) bool {
	h := sshProbeHost(host)
	if h == "" {
		return false
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(h, "22"))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ---------------------------------------------------------------------------
// Environment detection
// ---------------------------------------------------------------------------

type envProbe struct {
	OS               string // "darwin", "linux"
	BrewPresent      bool   // macOS-only
	DockerReachable  bool
	DejimadInstalled bool
	DejimaInstalled  bool
	TailscalePresent bool
	HostTailnetFQDN  string
	DaemonReachable  bool
}

func detectEnv() *envProbe {
	e := &envProbe{OS: runtime.GOOS}
	if e.OS == "darwin" {
		if _, err := exec.LookPath("brew"); err == nil {
			e.BrewPresent = true
		}
	}
	if err := exec.Command("docker", "version").Run(); err == nil {
		e.DockerReachable = true
	}
	if _, err := exec.LookPath("dejimad"); err == nil {
		e.DejimadInstalled = true
	}
	if self, err := os.Executable(); err == nil {
		// dejima is presumably already installed if we're running. But the
		// user may have run from a build dir; check /usr/local too.
		if filepath.Dir(self) == "/usr/local/bin" {
			e.DejimaInstalled = true
		}
	}
	if _, err := exec.LookPath("tailscale"); err == nil {
		e.TailscalePresent = true
		if out, err := exec.Command("tailscale", "status", "--json").Output(); err == nil {
			var status struct {
				Self struct {
					DNSName string `json:"DNSName"`
				} `json:"Self"`
			}
			if json.Unmarshal(out, &status) == nil {
				e.HostTailnetFQDN = strings.TrimSuffix(status.Self.DNSName, ".")
			}
		}
	}
	// Daemon reachable? Try a quick health check on the local socket.
	if c, err := api_client(); err == nil {
		ctx, cancel := contextWithTimeout(2)
		defer cancel()
		if err := c.Health(ctx); err == nil {
			e.DaemonReachable = true
		}
	}
	return e
}

func printEnvSummary(e *envProbe) {
	fmt.Println("Detected:")
	fmt.Printf("  os:        %s\n", e.OS)
	fmt.Printf("  docker:    %s\n", okMark(e.DockerReachable))
	if e.OS == "darwin" {
		fmt.Printf("  homebrew:  %s\n", okMark(e.BrewPresent))
	}
	fmt.Printf("  dejimad:   %s\n", okMark(e.DejimadInstalled))
	fmt.Printf("  tailscale: %s%s\n", okMark(e.TailscalePresent), withFQDN(e.HostTailnetFQDN))
	fmt.Printf("  daemon:    %s\n", okMark(e.DaemonReachable))
	if e.DockerReachable {
		if host := vmmem.HostMemoryBytes(); host > 0 {
			if out, err := exec.Command("docker", "info", "--format", "{{.MemTotal}}").Output(); err == nil {
				if vm, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); vm > 0 {
					line := fmt.Sprintf("%s of %s host", humanBytes(vm), humanBytes(host))
					if vmmem.Undersized(host, vm) {
						line += fmt.Sprintf("  ⚠ too small — islands will OOM; run: colima start --memory %d", vmmem.RecommendedGB(host))
					}
					fmt.Printf("  vm ram:    %s\n", line)
				}
			}
		}
	}
}

func okMark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

func withFQDN(s string) string {
	if s == "" {
		return ""
	}
	return "  (" + s + ")"
}

// ---------------------------------------------------------------------------
// Scenario outputs
// ---------------------------------------------------------------------------

func printServerInstall(ctx context.Context, e *envProbe, alsoClient bool) error {
	fmt.Println()
	fmt.Println(bold("Server install on this machine"))
	fmt.Println()

	if e.DaemonReachable && e.DejimadInstalled {
		fmt.Println("Looks like Dejima is already installed and running here.")
		fmt.Println("If you want to reconfigure, see `dejima service uninstall` and re-run setup.")
		fmt.Println()
		if ans := readSingleKey("Set up remote access (Tailscale SSH + reach Dejima from other devices)? [Y/n]: "); ans == "" || strings.EqualFold(ans, "y") {
			return setupRemoteAccess(ctx, e)
		}
		return nil
	}

	steps := []string{}

	if e.OS == "darwin" && !e.BrewPresent {
		steps = append(steps,
			"# Install Homebrew first (one-time):\n/bin/bash -c \"$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\"")
	}
	if !e.DockerReachable {
		switch e.OS {
		case "darwin":
			steps = append(steps,
				"# Install Docker Desktop (free for personal + small business use):\nbrew install --cask docker-desktop\n# Launch /Applications/Docker.app once to grant macOS permissions.")
		case "linux":
			steps = append(steps,
				"# Install Docker engine via your distro:\n#   Debian/Ubuntu: sudo apt install docker.io\n#   Fedora:        sudo dnf install docker\n#   Arch:          sudo pacman -S docker\n# Then: sudo systemctl enable --now docker && sudo usermod -aG docker $USER")
		}
	}
	if !e.TailscalePresent {
		steps = append(steps,
			"# (Optional but recommended) Install Tailscale for multi-device access:\n#   macOS: brew install --cask tailscale-app\n#   Linux: see https://tailscale.com/download")
	}

	steps = append(steps,
		"# Clone the source if you don't already have it:\ngit clone https://github.com/aoos/dejima.git ~/code/dejima\ncd ~/code/dejima")

	steps = append(steps,
		"# Build, install, image, register daemon as a service. Idempotent — safe to re-run:\nmake setup")

	if alsoClient {
		fmt.Println("This machine will be the server. The CLI also installed here uses the")
		fmt.Println("local Unix socket — no DEJIMA_HOST env var needed for local use.")
		fmt.Println()
	}

	fmt.Println("Run these in order:")
	fmt.Println()
	for _, s := range steps {
		fmt.Println(indentBlock(s, "  "))
		fmt.Println()
	}

	if e.HostTailnetFQDN != "" {
		fmt.Println("Once the daemon is running, other devices on your tailnet can connect with:")
		fmt.Printf("    export DEJIMA_HOST=%s:7273\n\n", e.HostTailnetFQDN)
	}

	if offerToRunMakeSetup(e) {
		if doRun := readSingleKey("Run `make setup` now? [y/N]: "); strings.EqualFold(doRun, "y") {
			if err := execInteractive("make", "setup"); err != nil {
				return err
			}
			// Re-probe: make setup may have just installed Tailscale and the daemon.
			e = detectEnv()
		}
	}

	fmt.Println()
	if ans := readSingleKey("Set up remote access (Tailscale SSH + reach Dejima from other devices) now? [Y/n]: "); ans == "" || strings.EqualFold(ans, "y") {
		return setupRemoteAccess(ctx, e)
	}
	fmt.Println("Skipped. Run `dejima onboard` → option 5 anytime to set up remote access.")
	return nil
}

func printClientInstall(ctx context.Context, e *envProbe) error {
	fmt.Println()
	fmt.Println(bold("Connect to a remote Dejima host"))
	fmt.Println()
	fmt.Println("This machine runs the `dejima` CLI only — no daemon, no Docker. It talks")
	fmt.Println("to a Dejima host over your tailnet. You're already running the CLI, so it's")
	fmt.Println("installed — I'll point it at the host, persist that, and verify the link.")
	fmt.Println()

	if !e.TailscalePresent {
		fmt.Println("⚠ Tailscale isn't detected here. The host accepts only tailnet peers, so")
		fmt.Println("  install it and log into the same account first:")
		fmt.Println("    macOS: brew install --cask tailscale-app")
		fmt.Println("    Linux: https://tailscale.com/download")
		fmt.Println()
	}

	host := strings.TrimSpace(readSingleKey("Daemon host (e.g. minion.tail2f808e.ts.net): "))
	if host == "" {
		fmt.Println("Skipped (no host provided). Set it later: export DEJIMA_HOST=<host>:7273")
		return nil
	}
	if !strings.Contains(host, ":") {
		host = host + ":7273"
	}

	// Make it live for the rest of this process so the dashboard opened right
	// after the wizard connects to this host immediately.
	_ = os.Setenv("DEJIMA_HOST", host)

	// Persist to the shell rc so future shells inherit it.
	if rc := shellRCPath(); rc != "" {
		prompt := fmt.Sprintf("Persist `export DEJIMA_HOST=%s` to %s? [Y/n]: ", host, tildeify(rc))
		if ans := readSingleKey(prompt); ans == "" || strings.EqualFold(ans, "y") {
			line := fmt.Sprintf("export DEJIMA_HOST=%s", host)
			if err := appendLineIfAbsent(rc, line); err != nil {
				fmt.Fprintf(os.Stderr, "  couldn't write %s: %v\n", rc, err)
				fmt.Printf("  add it yourself: echo '%s' >> %s\n", line, tildeify(rc))
			} else {
				fmt.Printf("  wrote to %s (new shells pick it up; this session is already set)\n", tildeify(rc))
			}
		}
	}

	// Verify connectivity now so the user gets immediate feedback.
	fmt.Println()
	fmt.Printf("Checking %s …\n", host)
	if err := verifyDejimaHost(ctx); err != nil {
		fmt.Printf("  ✗ couldn't reach the daemon: %v\n", err)
		fmt.Println("  Common causes:")
		fmt.Println("    • The host's daemon isn't exposing TCP — on the host run:")
		fmt.Println("        dejima service install --tcp :7273")
		fmt.Println("    • Both machines aren't on the same tailnet — check `tailscale status`")
	} else {
		fmt.Println("  ✓ connected. `dejima ls` and the dashboard will use this host.")
	}

	// Same tailnet membership that lets the CLI reach the daemon also lets you
	// SSH into the host directly, if its owner enabled Tailscale SSH there.
	if host, _, ok := strings.Cut(host, ":"); ok && host != "" {
		if u := currentUnixUser(); u != "" {
			fmt.Println()
			fmt.Printf("Tip: you can also open a shell on the host with `ssh %s@%s`\n", u, host)
			fmt.Println("     (works if its owner ran the host's remote-access setup).")
		}
	}
	return nil
}

// shellRCPath picks the rc file to persist env exports into, based on $SHELL.
func shellRCPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch {
	case strings.Contains(os.Getenv("SHELL"), "zsh"):
		return filepath.Join(home, ".zshenv")
	case strings.Contains(os.Getenv("SHELL"), "bash"):
		return filepath.Join(home, ".bash_profile")
	default:
		return filepath.Join(home, ".profile")
	}
}

// tildeify shortens a path under $HOME to ~/… for friendlier display.
func tildeify(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// appendLineIfAbsent appends line (plus newline) to path, creating it if
// needed. No-op if an identical line is already present, so re-running the
// wizard doesn't pile up duplicate exports.
func appendLineIfAbsent(path, line string) error {
	if existing, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(l) == line {
				return nil
			}
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

// verifyDejimaHost builds a client from the (just-set) DEJIMA_HOST env and
// runs a short, bounded health check.
func verifyDejimaHost(ctx context.Context) error {
	c, err := api_client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return c.Health(ctx)
}

func printOverview() error {
	fmt.Println()
	fmt.Println(bold("Dejima at a glance"))
	fmt.Println()
	fmt.Println("Two pieces:")
	fmt.Println("  • dejimad — the daemon. Manages container islands. Lives on a host you control.")
	fmt.Println("  • dejima  — the CLI. Talks to the daemon over a Unix socket or Tailscale TCP.")
	fmt.Println()
	fmt.Println("Common setups:")
	fmt.Println("  • Mac mini at home → install full stack with `make setup`")
	fmt.Println("  • Laptop / desktop → install CLI only, point DEJIMA_HOST at the Mac mini")
	fmt.Println("  • Both — server here, CLI also used here → same as Mac mini setup")
	fmt.Println()
	fmt.Println("More:")
	fmt.Println("  Source:  https://github.com/aoos/dejima")
	fmt.Println("  Install: https://dejima.tech/")
	fmt.Println("  API:     https://dejima.tech/api.html")
	fmt.Println()
	fmt.Println("Re-run this wizard anytime with `dejima onboard`.")
	return nil
}

// ---------------------------------------------------------------------------
// Remote access — make this host reachable (Tailscale SSH + remote Dejima)
// ---------------------------------------------------------------------------

// setupRemoteAccess walks the user through making this machine reachable from
// their other devices, anywhere on the internet, over their tailnet:
//
//  1. Ensure Tailscale is installed and up, with its SSH server + MagicDNS on.
//  2. Add the tailnet ACL `ssh` rule (the one step the node can't self-apply) —
//     automatically via an API key if the user pastes one, else printed.
//  3. Ensure the Dejima daemon is exposed over TCP for remote `dejima connect`.
//  4. Print the exact steps for the *second* device (inherently off-box) and
//     the `ssh <user>@<host>` line, then verify peers are visible.
//
// Host-side steps run here; the genuinely off-box bits are printed.
func setupRemoteAccess(ctx context.Context, e *envProbe) error {
	fmt.Println()
	fmt.Println(bold("Remote access — reach this host from your other devices"))
	fmt.Println()
	fmt.Println("Dejima pins remote access to your tailnet: an encrypted overlay that")
	fmt.Println("traverses NAT, so \"from anywhere\" needs no port-forwarding or public IP.")
	fmt.Println("I'll set up the host side here; the only manual bits are your tailnet's")
	fmt.Println("SSH policy and joining your second device (both inherently off-box).")
	fmt.Println()

	if !ensureTailscale(e) {
		return nil
	}
	if !ensureTailscaleUpWithSSH(e) {
		return nil
	}

	unixUser := currentUnixUser()
	fqdn := e.HostTailnetFQDN // refreshed by ensureTailscaleUpWithSSH

	// 2. The tailnet ACL ssh rule — node can't set this; offer API automation.
	fmt.Println()
	fmt.Println(bold("Tailnet SSH policy"))
	configureSSHACL(ctx, unixUser)

	// 3. Expose the Dejima daemon over TCP so `dejima connect` works remotely.
	fmt.Println()
	fmt.Println(bold("Dejima daemon over the tailnet"))
	if e.DejimadInstalled {
		fmt.Println("Expose the daemon on a tailnet TCP port so remote clients can reach it:")
		fmt.Println(indentBlock("dejima service install --tcp :7273", "    "))
		if ans := readSingleKey("Run that now (reinstalls the service with TCP enabled)? [y/N]: "); strings.EqualFold(ans, "y") {
			if self, err := os.Executable(); err == nil {
				if err := execInteractive(self, "service", "install", "--tcp", ":7273"); err != nil {
					fmt.Printf("  ✗ %v — run it yourself when ready.\n", err)
				}
			}
		}
	} else {
		fmt.Println("(No daemon installed here yet — once you run `make setup`, expose it with")
		fmt.Println(" `dejima service install --tcp :7273` to allow remote `dejima connect`.)")
	}

	// 4. The off-box steps + how to connect.
	printRemoteAccessNextSteps(fqdn, unixUser)
	verifyTailnetPeers()
	return nil
}

// ensureTailscale makes sure the tailscale binary is present, offering to
// install it on macOS. Returns false (with guidance printed) if it's still
// missing afterward, so the caller can stop.
func ensureTailscale(e *envProbe) bool {
	if e.TailscalePresent {
		return true
	}
	fmt.Println("Tailscale isn't installed here yet.")
	switch e.OS {
	case "darwin":
		if e.BrewPresent {
			if ans := readSingleKey("Install it now with `brew install --cask tailscale-app`? [Y/n]: "); ans == "" || strings.EqualFold(ans, "y") {
				stopSudo := primeSudo("Installing Tailscale")
				err := execInteractive("brew", "install", "--cask", "tailscale-app")
				stopSudo()
				if err != nil {
					fmt.Printf("  ✗ install failed: %v\n", err)
				}
			}
		} else {
			fmt.Println("  Install it: brew install --cask tailscale-app  (or https://tailscale.com/download)")
		}
	case "linux":
		fmt.Println("  Install it: curl -fsSL https://tailscale.com/install.sh | sh")
	}
	if _, err := exec.LookPath("tailscale"); err != nil {
		fmt.Println("  Once Tailscale is installed, re-run `dejima onboard` → option 5.")
		return false
	}
	e.TailscalePresent = true
	return true
}

// ensureTailscaleUpWithSSH brings the tailnet up (if needed) and turns on the
// Tailscale SSH server and MagicDNS. `tailscale up --ssh` triggers a browser
// login when the node isn't authenticated; `tailscale set` is used when it's
// already up so we don't force a re-login. Refreshes e.HostTailnetFQDN.
func ensureTailscaleUpWithSSH(e *envProbe) bool {
	st := tailscaleStatus()
	switch st.BackendState {
	case "Running":
		fmt.Println("Tailscale is up. Enabling its SSH server + MagicDNS (idempotent)…")
		if err := execInteractive("tailscale", "set", "--ssh", "--accept-dns=true"); err != nil {
			fmt.Printf("  ✗ `tailscale set` failed: %v\n", err)
			return false
		}
	default: // "NeedsLogin", "Stopped", "NoState", empty
		fmt.Println("Bringing Tailscale up with its SSH server enabled.")
		fmt.Println("A browser window will open — log in as the account that owns this tailnet.")
		fmt.Println()
		if err := execInteractive("tailscale", "up", "--ssh", "--accept-dns=true"); err != nil {
			fmt.Printf("  ✗ `tailscale up` failed: %v\n", err)
			fmt.Println("  Run it yourself, then re-run option 5: tailscale up --ssh --accept-dns=true")
			return false
		}
	}
	// Refresh the FQDN now that we're (re)connected.
	if st := tailscaleStatus(); st.Self.DNSName != "" {
		e.HostTailnetFQDN = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	if e.HostTailnetFQDN != "" {
		fmt.Printf("  ✓ Tailscale SSH on. This host is %s\n", e.HostTailnetFQDN)
	} else {
		fmt.Println("  ✓ Tailscale SSH enabled.")
	}
	return true
}

// tsStatus is the slice of `tailscale status --json` we care about.
type tsStatus struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		DNSName string `json:"DNSName"`
	} `json:"Self"`
	Peer map[string]json.RawMessage `json:"Peer"`
}

func tailscaleStatus() tsStatus {
	var st tsStatus
	if out, err := exec.Command("tailscale", "status", "--json").Output(); err == nil {
		_ = json.Unmarshal(out, &st)
	}
	return st
}

// configureSSHACL adds (or guides the user to add) a tailnet `ssh` rule that
// permits logging in as unixUser. The rule lives in the Tailscale control
// plane, so the node can't set it directly: offer API-key automation, falling
// back to printing the block plus the admin deep link.
func configureSSHACL(ctx context.Context, unixUser string) {
	fmt.Println("Tailscale SSH refuses every connection unless your tailnet policy has an")
	fmt.Println("`ssh` rule. That policy lives in the Tailscale admin console, not on this")
	fmt.Println("machine — so either paste an API key and I'll add the rule, or I'll print")
	fmt.Println("it for you to paste.")
	fmt.Println()
	key := readSecret("Tailscale API key (tskey-api-…, or Enter to skip): ")
	if key == "" {
		printACLManual(unixUser)
		return
	}
	if err := applySSHACL(ctx, key, unixUser); err != nil {
		fmt.Printf("  ✗ couldn't update the policy automatically: %v\n", err)
		fmt.Println()
		printACLManual(unixUser)
		return
	}
	fmt.Printf("  ✓ added an SSH rule to your tailnet policy (login as %q).\n", unixUser)
}

// applySSHACL fetches the current tailnet policy via the Tailscale API and, if
// it has no `ssh` section yet, appends a rule permitting unixUser. It will not
// touch a policy that already has ssh rules — that's the user's domain. Uses
// the `-` tailnet alias (the API key's default tailnet) and an If-Match ETag
// to avoid clobbering a concurrent edit.
func applySSHACL(ctx context.Context, apiKey, unixUser string) error {
	const base = "https://api.tailscale.com/api/v2/tailnet/-/acl"
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// GET current policy as canonical JSON, capturing the ETag.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(apiKey, "")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET policy: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	etag := resp.Header.Get("ETag")

	var policy map[string]any
	if err := json.Unmarshal(body, &policy); err != nil {
		return fmt.Errorf("parse policy: %w", err)
	}
	if existing, ok := policy["ssh"].([]any); ok && len(existing) > 0 {
		return fmt.Errorf("your policy already has an ssh section — leaving it untouched "+
			"(confirm it permits user %q)", unixUser)
	}

	fmt.Println("  I'll append an ssh rule to your tailnet policy. The whole policy is")
	fmt.Println("  rewritten via the API, which normalizes formatting and strips comments.")
	if ans := readSingleKey("  Proceed? [y/N]: "); !strings.EqualFold(ans, "y") {
		return fmt.Errorf("cancelled by user")
	}

	policy["ssh"] = []any{sshACLRule(unixUser)}
	updated, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodPost, base, bytes.NewReader(updated))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	req.SetBasicAuth(apiKey, "")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST policy: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// sshACLRule is the rule we add: any tailnet member may SSH into their own
// devices as unixUser (or any non-root local user).
func sshACLRule(unixUser string) map[string]any {
	users := []string{"autogroup:nonroot"}
	if unixUser != "" {
		users = []string{unixUser, "autogroup:nonroot"}
	}
	return map[string]any{
		"action": "accept",
		"src":    []string{"autogroup:member"},
		"dst":    []string{"autogroup:self"},
		"users":  users,
	}
}

func printACLManual(unixUser string) {
	rule := sshACLRule(unixUser)
	block, _ := json.MarshalIndent([]any{rule}, "  ", "  ")
	fmt.Println("  Add this to your tailnet policy at")
	fmt.Println("    https://login.tailscale.com/admin/acls")
	fmt.Println("  under a top-level \"ssh\": key —")
	fmt.Println()
	fmt.Printf("  \"ssh\": %s\n", string(block))
	fmt.Println()
	fmt.Println("  (action \"accept\" connects with no prompt; use \"check\" for periodic re-auth.)")
}

// printRemoteAccessNextSteps prints the inherently off-box steps: joining the
// second device and the resulting connection commands.
func printRemoteAccessNextSteps(fqdn, unixUser string) {
	short := fqdn
	if i := strings.IndexByte(fqdn, '.'); i > 0 {
		short = fqdn[:i]
	}
	if unixUser == "" {
		unixUser = "<you>"
	}
	fmt.Println()
	fmt.Println(bold("On each device you want to connect FROM"))
	fmt.Println("(this part is on the other machine — it can't be done from here):")
	fmt.Println()
	fmt.Println("  1. Install Tailscale:")
	fmt.Println("       macOS:   brew install --cask tailscale-app")
	fmt.Println("       Linux:   curl -fsSL https://tailscale.com/install.sh | sh")
	fmt.Println("       Windows/iOS/Android: https://tailscale.com/download")
	fmt.Println("  2. Log into the SAME account that owns this host:")
	fmt.Println("       tailscale up")
	fmt.Println("  3. Then, from anywhere:")
	if fqdn != "" {
		fmt.Printf("       ssh %s@%s          # MagicDNS short name\n", unixUser, short)
		fmt.Printf("       ssh %s@%s   # full name if short doesn't resolve\n", unixUser, fqdn)
		fmt.Printf("       export DEJIMA_HOST=%s:7273 && dejima ls   # drive Dejima remotely\n", fqdn)
	} else {
		fmt.Printf("       ssh %s@<this-host>\n", unixUser)
	}
}

// verifyTailnetPeers reports whether any other devices are on the tailnet yet,
// since SSH-from-anywhere needs at least one device to connect *from*.
func verifyTailnetPeers() {
	st := tailscaleStatus()
	fmt.Println()
	if len(st.Peer) == 0 {
		fmt.Println("Note: no other devices are on your tailnet yet — add one with the steps")
		fmt.Println("above, and it'll be able to reach this host immediately.")
	} else {
		fmt.Printf("✓ %d other device(s) already on your tailnet can reach this host now.\n", len(st.Peer))
	}
}

// currentUnixUser returns the local login name to SSH in as.
func currentUnixUser() string {
	if u, err := osuser.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// readSecret prompts for a value without echoing it to the terminal.
func readSecret(prompt string) string {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return readSingleKey("")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func bold(s string) string {
	return "\033[1m" + s + "\033[0m"
}

func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func offerToRunMakeSetup(e *envProbe) bool {
	// Only offer if we're sitting next to a Makefile and have brew (mac case)
	// or can reasonably proceed (linux case).
	if _, err := os.Stat("Makefile"); err != nil {
		return false
	}
	if e.OS == "darwin" && !e.BrewPresent {
		return false
	}
	return true
}

func execInteractive(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// Indirections so this file's helpers can be reused without importing
// `cmd/dejima/main.go`'s package-private helpers from elsewhere.
var (
	api_client         = client
	contextWithTimeout = func(seconds int) (context.Context, context.CancelFunc) {
		return context.WithCancel(context.Background())
	}
)
