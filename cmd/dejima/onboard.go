package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/vmmem"
)

// newOnboardCmd is the explicit re-entry into the wizard. Always runs,
// regardless of whether the dismissal marker exists.
func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Walk through Dejima setup (run anytime to (re)configure).",
		Long: "Interactive wizard. Detects what's already on this machine, asks what " +
			"you're trying to do, and prints a tailored set of commands. Safe to run " +
			"more than once.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runOnboarding(cmd.Context()); err != nil {
				return err
			}
			// Explicit `dejima onboard` also dismisses the first-run prompt —
			// the user has clearly seen the wizard.
			_ = writeDismissalMarker()
			return nil
		},
	}
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

	fmt.Println()
	fmt.Println(bold("First time on this machine?"))
	fmt.Println()
	fmt.Println("  I can walk you through setting up Dejima — Docker check, daemon")
	fmt.Println("  install, notification webhook, etc.")
	fmt.Println()
	fmt.Println("    y) Yes, walk me through it")
	fmt.Println("    n) Not now — ask me again next time")
	fmt.Println("    N) Never ask again (re-run anytime with `dejima onboard`)")
	fmt.Println()

	switch readSingleKey("Choice [y/n/N]: ") {
	case "y", "Y", "yes":
		if err := runOnboarding(ctx); err != nil {
			return false, err
		}
		_ = writeDismissalMarker()
		return true, nil
	case "N", "never":
		fmt.Println("Got it. Re-engage anytime with `dejima onboard`.")
		if err := writeDismissalMarker(); err != nil {
			return false, err
		}
		return true, nil
	case "n", "no", "later", "":
		fmt.Println("OK — opening the dashboard. Run `dejima onboard` anytime to walk through setup.")
		return true, nil
	default:
		fmt.Println("Didn't catch that — treating as 'not now'.")
		return true, nil
	}
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
	fmt.Println()

	switch readSingleKey("Choice [1/2/3/4/5]: ") {
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
	default:
		fmt.Println("No choice made. Re-run anytime with `dejima onboard`.")
		return nil
	}
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
				"# Install Docker Desktop (free for personal + small business use):\nbrew install --cask docker\n# Launch /Applications/Docker.app once to grant macOS permissions.")
		case "linux":
			steps = append(steps,
				"# Install Docker engine via your distro:\n#   Debian/Ubuntu: sudo apt install docker.io\n#   Fedora:        sudo dnf install docker\n#   Arch:          sudo pacman -S docker\n# Then: sudo systemctl enable --now docker && sudo usermod -aG docker $USER")
		}
	}
	if !e.TailscalePresent {
		steps = append(steps,
			"# (Optional but recommended) Install Tailscale for multi-device access:\n#   macOS: brew install --cask tailscale\n#   Linux: see https://tailscale.com/download")
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
		fmt.Println("    macOS: brew install --cask tailscale")
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
	fmt.Println("  Install: https://aoos.github.io/dejima/")
	fmt.Println("  API:     https://aoos.github.io/dejima/api.html")
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
			if ans := readSingleKey("Install it now with `brew install --cask tailscale`? [Y/n]: "); ans == "" || strings.EqualFold(ans, "y") {
				if err := execInteractive("brew", "install", "--cask", "tailscale"); err != nil {
					fmt.Printf("  ✗ install failed: %v\n", err)
				}
			}
		} else {
			fmt.Println("  Install it: brew install --cask tailscale  (or https://tailscale.com/download)")
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
	fmt.Println("       macOS:   brew install --cask tailscale")
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
