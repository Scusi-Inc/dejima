package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/aoos/dejima/internal/paths"
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
//   - "yes"     → runs the wizard, then returns (false, nil) so the user
//                 acts on the wizard's output rather than dropping into the
//                 TUI immediately.
//   - "not now" → returns (false, nil); marker NOT written, so the prompt
//                 reappears on the next run.
//   - "never"   → writes the marker and returns (true, nil); the TUI opens
//                 normally from here on.
//   - non-TTY   → treated as "not now" (no prompt fires, no marker written).
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
		return false, nil
	case "N", "never":
		fmt.Println("Got it. Re-engage anytime with `dejima onboard`.")
		if err := writeDismissalMarker(); err != nil {
			return false, err
		}
		return true, nil
	case "n", "no", "later", "":
		fmt.Println("OK. Run `dejima onboard` anytime to walk through setup.")
		return false, nil
	default:
		fmt.Println("Didn't catch that — treating as 'not now'.")
		return false, nil
	}
}

// readSingleKey prompts and reads a line of input from stdin.
func readSingleKey(prompt string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
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
	fmt.Println()

	switch readSingleKey("Choice [1/2/3/4]: ") {
	case "1":
		return printServerInstall(env, false)
	case "2":
		return printClientInstall(env)
	case "3":
		return printServerInstall(env, true)
	case "4":
		return printOverview()
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

func printServerInstall(e *envProbe, alsoClient bool) error {
	fmt.Println()
	fmt.Println(bold("Server install on this machine"))
	fmt.Println()

	if e.DaemonReachable && e.DejimadInstalled {
		fmt.Println("Looks like Dejima is already installed and running here.")
		fmt.Println("If you want to reconfigure, see `dejima service uninstall` and re-run setup.")
		fmt.Println()
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
			return execInteractive("make", "setup")
		}
	}

	return nil
}

func printClientInstall(e *envProbe) error {
	fmt.Println()
	fmt.Println(bold("Client-only install"))
	fmt.Println()
	fmt.Println("This machine will run the `dejima` CLI only — no daemon, no Docker needed.")
	fmt.Println("The CLI talks to a remote Dejima host over your tailnet.")
	fmt.Println()

	host := strings.TrimSpace(readSingleKey("Daemon host (e.g. minion.tail2f808e.ts.net): "))
	if host == "" {
		fmt.Println("Skipped (no host provided). You can set it later with `export DEJIMA_HOST=<host>:7273`.")
	} else if !strings.Contains(host, ":") {
		host = host + ":7273"
	}

	steps := []string{
		"# Install the dejima CLI (requires Go 1.22+):\ngo install github.com/aoos/dejima/cmd/dejima@latest",
	}
	if host != "" {
		steps = append(steps,
			fmt.Sprintf("# Point at the host. Add to your shell rc so it persists:\nexport DEJIMA_HOST=%s\necho 'export DEJIMA_HOST=%s' >> ~/.zshenv", host, host))
		steps = append(steps,
			"# Verify:\ndejima doctor")
	}

	fmt.Println()
	fmt.Println("Run these in order:")
	fmt.Println()
	for _, s := range steps {
		fmt.Println(indentBlock(s, "  "))
		fmt.Println()
	}
	return nil
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
