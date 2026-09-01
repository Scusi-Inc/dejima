package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/selfupdate"
	"github.com/aoos/dejima/internal/version"
	"github.com/aoos/dejima/internal/wsl"
)

// newWSLCmd is the Windows "run it locally after all" surface.
//
// Windows can't host dejimad (Unix + Docker only), which used to make "local"
// a dead end for every Windows user: the daemon-unreachable help pointed at
// `dejimad --foreground` and `dejima service install`, neither of which exists
// there. WSL2 closes that gap — it is a real Linux kernel with a real Docker on
// the same machine — so this command provisions a distro into a working Dejima
// host and saves it as a connection profile (`wsl://<distro>`).
//
// The client reaches it by tunnelling dejimad's Unix socket through
// `wsl.exe … socat` (see internal/wsl), so the daemon keeps its 0600-socket
// trust model: no TCP listener, no token, no relaxation of the tailnet pin.
func newWSLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wsl",
		Short: "Run a Dejima host locally on Windows, inside WSL2.",
		Long: "Windows can't run dejimad — it needs a Unix host with Docker. WSL2 is one, on this " +
			"same machine, so `dejima wsl setup` installs Docker + dejimad into a WSL2 distro and " +
			"connects this client to it over the distro's Unix socket (no TCP listener, no token).\n\n" +
			"Bare `dejima wsl` shows status.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runWSLStatus(cmd.Context(), "") },
	}
	cmd.AddCommand(newWSLStatusCmd(), newWSLSetupCmd(), newWSLStartCmd(), newWSLStopCmd())
	return cmd
}

// requireWSLPlatform rejects the command off Windows with the reason, not a
// confusing downstream "wsl.exe: not found".
func requireWSLPlatform() error {
	if !wsl.Supported() {
		return fmt.Errorf("`dejima wsl` is a Windows-only path (this is %s) — on a Unix host, "+
			"dejimad runs natively: `dejima onboard`", runtime.GOOS)
	}
	if !wsl.Available() {
		return fmt.Errorf("WSL isn't installed. Install it (an admin PowerShell, then reboot):\n"+
			"    %s\n"+
			"then re-run `dejima wsl setup`", wsl.InstallHint)
	}
	return nil
}

func newWSLStatusCmd() *cobra.Command {
	var distro string
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show the WSL2 distro's readiness as a Dejima host.",
		Aliases: []string{"st"},
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return runWSLStatus(cmd.Context(), distro) },
	}
	cmd.Flags().StringVar(&distro, "distro", "", "WSL distro to inspect (default: "+wsl.DefaultDistro+")")
	return cmd
}

func runWSLStatus(parent context.Context, distro string) error {
	if err := requireWSLPlatform(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()

	if distro == "" {
		distro = wsl.DefaultDistro
	}
	rep, err := wsl.Probe(ctx, distro)
	if err != nil {
		return err
	}
	if !rep.Exists {
		fmt.Printf("distro %q: not installed\n\n", distro)
		dists, _ := wsl.List(ctx)
		if len(dists) > 0 {
			fmt.Println("installed distros:")
			printDistroTable(dists)
			fmt.Println()
		}
		fmt.Println("create it and install the daemon:  dejima wsl setup")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "distro:\t%s\n", rep.Distro)
	fmt.Fprintf(w, "wsl version:\t%d%s\n", rep.Version, wslVersionNote(rep.Version))
	fmt.Fprintf(w, "state:\t%s\n", map[bool]string{true: "running", false: "stopped"}[rep.Running])
	fmt.Fprintf(w, "socat:\t%s\n", checkMark(rep.HasSocat, "installed", "missing — the client tunnels the socket through it"))
	fmt.Fprintf(w, "docker:\t%s\n", checkMark(rep.HasDocker, "engine responding", "not responding — islands can't start"))
	fmt.Fprintf(w, "dejimad:\t%s\n", checkMark(rep.HasDejima, "installed", "not installed"))
	fmt.Fprintf(w, "socket:\t%s\n", checkMark(rep.SocketUp, "up (~/.dejima/dejimad.sock)", "daemon not running"))
	_ = w.Flush()

	fmt.Println()
	switch {
	case rep.Ready():
		fmt.Printf("ready — connect with:  dejima profile switch %s\n", profileNameFor(distro))
		if active := activeWSLProfile(); active != "" {
			fmt.Printf("(currently connected via profile %q)\n", active)
		}
	case rep.Version == 1:
		fmt.Printf("this distro is WSL 1 — convert it:  wsl --set-version %s 2\n", distro)
	default:
		fmt.Println("not ready — finish setup:  dejima wsl setup")
	}
	return nil
}

func wslVersionNote(v int) string {
	switch v {
	case 2:
		return ""
	case 1:
		return "  (WSL 1 has no real kernel — Docker won't run; needs `wsl --set-version <distro> 2`)"
	default:
		return "  (unknown)"
	}
}

func checkMark(ok bool, yes, no string) string {
	if ok {
		return "OK    " + yes
	}
	return "MISSING  " + no
}

func printDistroTable(ds []wsl.Distribution) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  NAME\tSTATE\tWSL")
	for _, d := range ds {
		star := "  "
		if d.Default {
			star = "* "
		}
		fmt.Fprintf(w, "%s%s\t%s\t%d\n", star, d.Name, d.State, d.Version)
	}
	_ = w.Flush()
}

// profileNameFor is the connection-profile name we save for a distro. Distinct
// from the distro name so `wsl` reads as the transport in `profile ls`.
func profileNameFor(distro string) string {
	if distro == "" || distro == wsl.DefaultDistro {
		return "wsl"
	}
	return "wsl-" + distro
}

// activeWSLProfile returns the name of the active profile if it points at a WSL
// distro, else "".
func activeWSLProfile() string {
	cfg, err := clientcfg.Load()
	if err != nil {
		return ""
	}
	h, ok := cfg.ActiveHost()
	if !ok || !wsl.IsHost(h) {
		return ""
	}
	return cfg.ActiveProfile
}

func newWSLStartCmd() *cobra.Command {
	var distro string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start dejimad inside the WSL2 distro.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireWSLPlatform(); err != nil {
				return err
			}
			if distro == "" {
				distro = wsl.DefaultDistro
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			if err := startDaemonInWSL(ctx, distro); err != nil {
				return err
			}
			fmt.Println("dejimad is running in", distro)
			return nil
		},
	}
	cmd.Flags().StringVar(&distro, "distro", "", "WSL distro (default: "+wsl.DefaultDistro+")")
	return cmd
}

func newWSLStopCmd() *cobra.Command {
	var distro string
	var shutdown bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop dejimad inside the WSL2 distro (islands keep their state).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireWSLPlatform(); err != nil {
				return err
			}
			if distro == "" {
				distro = wsl.DefaultDistro
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			if _, err := wsl.Run(ctx, distro, `pkill -x dejimad 2>/dev/null; exit 0`); err != nil {
				return err
			}
			fmt.Println("stopped dejimad in", distro)
			if shutdown {
				// Terminating the distro frees the VM's RAM. Islands survive: their
				// containers and volumes are on the distro's disk, and `dejima wsl
				// start` (or setup) brings the daemon back.
				if _, err := wsl.RunExe(ctx, "--terminate", distro); err != nil {
					return fmt.Errorf("terminate %s: %w", distro, err)
				}
				fmt.Printf("terminated the %s VM — its memory is released; islands are intact\n", distro)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&distro, "distro", "", "WSL distro (default: "+wsl.DefaultDistro+")")
	cmd.Flags().BoolVar(&shutdown, "shutdown", false, "also terminate the distro's VM, releasing its memory (islands are unaffected)")
	return cmd
}

// ---------------------------------------------------------------------------
// setup
// ---------------------------------------------------------------------------

func newWSLSetupCmd() *cobra.Command {
	var (
		distro string
		yes    bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Provision a WSL2 distro into a working Dejima host and connect to it.",
		Long: "Installs, in a WSL2 distro: Docker engine, socat (the socket tunnel the Windows " +
			"client uses), and dejimad. Then starts the daemon, builds the island image, and saves " +
			"a `wsl://<distro>` connection profile as your active target.\n\n" +
			"Idempotent — safe to re-run after a partial or failed setup.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWSLSetup(cmd.Context(), distro, yes)
		},
	}
	cmd.Flags().StringVar(&distro, "distro", "", "WSL distro to use or create (default: "+wsl.DefaultDistro+")")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't prompt; accept the installs")
	return cmd
}

func runWSLSetup(parent context.Context, distro string, yes bool) error {
	if err := requireWSLPlatform(); err != nil {
		return err
	}
	if distro == "" {
		distro = wsl.DefaultDistro
	}
	// Generous: a first run installs a distro, the Docker engine, and builds the
	// island image.
	ctx, cancel := context.WithTimeout(parent, 45*time.Minute)
	defer cancel()

	fmt.Printf("Setting up a Dejima host in WSL2 (distro %q).\n\n", distro)

	rep, err := wsl.Probe(ctx, distro)
	if err != nil {
		return err
	}

	if !rep.Exists {
		fmt.Printf("Distro %q doesn't exist yet. Dejima will create it from the Ubuntu image.\n", distro)
		if !yes && !confirmWSL("Create it now?") {
			return fmt.Errorf("cancelled — create a distro yourself and re-run with --distro <name>")
		}
		if err := createDistro(ctx, distro); err != nil {
			return err
		}
		rep, err = wsl.Probe(ctx, distro)
		if err != nil {
			return err
		}
	}
	if rep.Version == 1 {
		return fmt.Errorf("distro %q is WSL 1, which has no real kernel and can't run Docker.\n"+
			"Convert it:  wsl --set-version %s 2\nthen re-run `dejima wsl setup`", distro, distro)
	}

	// Each step is idempotent and skipped when already satisfied, so a re-run
	// after a failure resumes rather than redoing everything.
	steps := []struct {
		name string
		skip bool
		run  func() error
	}{
		{"socat (the socket tunnel)", rep.HasSocat, func() error { return installSocat(ctx, distro) }},
		{"Docker engine", rep.HasDocker, func() error { return installDocker(ctx, distro, yes) }},
		{"dejimad", rep.HasDejima, func() error { return installDejimad(ctx, distro) }},
	}
	for _, s := range steps {
		if s.skip {
			fmt.Printf("  ✓ %s — already present\n", s.name)
			continue
		}
		fmt.Printf("  • installing %s …\n", s.name)
		if err := s.run(); err != nil {
			return fmt.Errorf("install %s: %w", s.name, err)
		}
		fmt.Printf("  ✓ %s\n", s.name)
	}

	fmt.Println("  • starting dejimad …")
	if err := startDaemonInWSL(ctx, distro); err != nil {
		return err
	}
	fmt.Println("  ✓ dejimad running")

	// The island image, which used to arrive free with the source build's
	// `make setup`. Without it a distro has a daemon and no image, and the
	// operator's first `dejima init` fails AFTER a setup that said it worked.
	//
	// After the daemon starts, because `dejima image build` talks to it. Slow on
	// a first run and near-instant afterwards, so it is announced rather than
	// silent — a multi-minute quiet stretch is the thing people Ctrl-C.
	fmt.Println("  • building the island image (first run is slow) …")
	if err := buildIslandImage(ctx, distro); err != nil {
		return fmt.Errorf("build island image: %w", err)
	}
	fmt.Println("  ✓ island image")

	if err := saveWSLProfile(distro); err != nil {
		return fmt.Errorf("save connection profile: %w", err)
	}
	name := profileNameFor(distro)
	fmt.Printf("  ✓ connected — profile %q → %s\n", name, wsl.Host(distro))

	// Verify end to end rather than declaring success on the install steps: the
	// whole point of this command is a client that can actually talk to it.
	fmt.Println("\nVerifying the connection …")
	c, err := clientForHost(wsl.Host(distro))
	if err != nil {
		return err
	}
	hctx, hcancel := context.WithTimeout(ctx, 60*time.Second)
	defer hcancel()
	if err := c.Health(hctx); err != nil {
		return fmt.Errorf("the daemon is installed but this client can't reach it: %w\n"+
			"check with:  dejima wsl status", err)
	}
	fmt.Println("  ✓ dejimad answered")

	fmt.Printf("\nDone. `dejima` now talks to the daemon in %s.\n", distro)
	fmt.Println("Next:  dejima            (the dashboard)")
	fmt.Println("       dejima wsl status (health of the WSL host)")
	fmt.Println("\nNote: your repos live on the Windows filesystem; islands clone from git, so")
	fmt.Println("that's fine. Working directly off /mnt/c is slow — prefer a git remote.")
	return nil
}

// createDistro installs Ubuntu under the requested distro name. --no-launch
// keeps it non-interactive (no first-boot username prompt); the distro then
// runs as root, which is what our unattended installs need.
func createDistro(ctx context.Context, distro string) error {
	fmt.Println("  • installing Ubuntu (this downloads a few hundred MB) …")
	out, err := runWSLExe(ctx, "--install", "-d", "Ubuntu", "--name", distro, "--no-launch")
	if err != nil {
		// Older wsl.exe builds lack --name/--no-launch. Say so precisely rather
		// than leaving the user with an opaque usage dump.
		if strings.Contains(strings.ToLower(out), "unrecognized") || strings.Contains(out, "Invalid command line") {
			return fmt.Errorf("this wsl.exe is too old to create a named distro (%w).\n"+
				"Update WSL:  wsl --update\n"+
				"Or create one yourself and pass it:  dejima wsl setup --distro <existing-distro>", err)
		}
		return err
	}
	return nil
}

// wslStepErr attaches what the distro actually said to a failed step.
//
// d4's finding, and the problem is worth stating exactly: wsl.Run returns the
// combined output AND an error, and every install step here discarded the
// output. So a failure inside the distro surfaced as "install Docker engine:
// exit status 1" — the one fact the operator already knew — while the apt error,
// the DNS failure, the disk-full message that actually explains it went into a
// variable nobody read. EVERY FAILURE ON THIS PATH WAS INVISIBLE, on the path
// with the worst network and the least ability to reproduce.
//
// Tailed rather than whole: an image build log is thousands of lines, and
// dumping all of it into a terminal buries the last twenty that matter.
func wslStepErr(out string, err error) error {
	if err == nil {
		return nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return err
	}
	return fmt.Errorf("%w\n--- last output from inside the distro ---\n%s",
		err, lastLines(out, 20))
}

// lastLines returns the final n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// socatInstallScript installs socat, the tunnel the Windows client uses to reach
// the daemon's unix socket. Without it the client cannot talk to the daemon at
// all, which presents as "daemon unreachable" and looks like the daemon is down.
//
// EVERY PACKAGE COMMAND IS TRIED DIRECTLY BEFORE SUDO. The previous version led
// with `sudo -n`, which assumes sudo is installed. A WSL distro commonly runs as
// ROOT — this one does; its HOME is /root — and a minimal root image need not
// ship sudo at all. There, `sudo -n apt-get` fails with "command not found"
// under `set -e` for a machine that never needed sudo in the first place.
//
// The same try-then-elevate shape is already used by the daemon install below,
// which is why that step kept working on the distro where this one did not.
const socatInstallScript = `
		set -e
		if command -v apt-get >/dev/null 2>&1; then
			(apt-get update -qq && apt-get install -y -qq socat) ||
				(sudo -n apt-get update -qq && sudo -n apt-get install -y -qq socat)
		elif command -v dnf >/dev/null 2>&1; then
			dnf install -y -q socat || sudo -n dnf install -y -q socat
		elif command -v apk >/dev/null 2>&1; then
			apk add --quiet socat || sudo -n apk add --quiet socat
		else
			echo "no supported package manager (need apt/dnf/apk)" >&2; exit 1
		fi`

func installSocat(ctx context.Context, distro string) error {
	out, err := wsl.Run(ctx, distro, socatInstallScript)
	return wslStepErr(out, err)
}

// installDocker runs Docker's official convenience script inside the distro and
// puts the user in the docker group. WSL2 has no systemd by default on older
// images, so we also make sure the daemon can be started by hand.
func installDocker(ctx context.Context, distro string, yes bool) error {
	// The login name is resolved in Go and substituted in. A `$(id -un)` here
	// would have expanded to nothing through this channel and `|| true` would
	// have swallowed the resulting usermod error — leaving the user quietly out
	// of the docker group. Silent-and-swallowed is the failure mode that lasts
	// longest; nothing would have looked wrong until something needed the group.
	who, werr := wsl.Run(ctx, distro, "id -un")
	who = strings.TrimSpace(who)
	if werr != nil || who == "" {
		who = "root" // the WSL default; better than an empty usermod target
	}
	if !yes && !confirmWSL("Install the Docker engine inside "+distro+" (get.docker.com)?") {
		return fmt.Errorf("cancelled — install Docker in the distro yourself, then re-run")
	}
	out, err := wsl.Run(ctx, distro, strings.ReplaceAll(`
		set -e
		if ! command -v docker >/dev/null 2>&1; then
			curl -fsSL https://get.docker.com | sudo -n sh
		fi
		sudo -n usermod -aG docker "USER_PLACEHOLDER" || true
		# systemd is off in older WSL images; start dockerd directly if so.
		if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
			sudo -n systemctl enable --now docker
		else
			sudo -n service docker start || (sudo -n dockerd >/tmp/dockerd.log 2>&1 &)
			sleep 5
		fi
		docker info >/dev/null 2>&1`, "USER_PLACEHOLDER", who))
	if err != nil {
		// The distro's own output first — a docker install fails for reasons only
		// it can state (no DNS, disk full, an apt lock) — then the command to look
		// deeper. The hint alone told the operator to go find what we already had.
		return fmt.Errorf("%w\n(check inside the distro:  wsl -d %s -- docker info)",
			wslStepErr(out, err), distro)
	}
	return nil
}

// installDejimad installs the daemon from a RELEASE TARBALL, not from source.
//
// It used to run `curl … install.sh | bash`, which is a source build — and
// install.sh installs Go on macOS and FAILS on Linux:
//
//	✗ Go is required. Install from https://go.dev/dl/ …
//
// A freshly created Ubuntu distro has no Go, so this step could never succeed on
// a first run. `dejima wsl setup` had never worked end to end; the one person who
// got a daemon up did it by hand.
//
// The release path is also the right one for the network this runs on. A source
// build clones a repo and downloads a module graph — hundreds of round-trips —
// and WSL's NAT is where that fails: in one session it produced a go.dev 404, a
// connection reset mid-install, and an empty GitHub API response. This makes ONE
// request.
//
// VERSION IS PINNED TO THIS CLIENT, not resolved to "latest". I argued the other
// way when this was someone else's task and was weighing the wrong thing:
// resolving latest costs an extra API call on precisely the flaky link this
// change exists to stop depending on, and it can hand a client a daemon from the
// far side of a release boundary. Pinning needs no lookup and makes the pair
// coherent by construction. A dev build has no matching release, so it falls
// back to latest — which is the only case where the extra call is unavoidable.
func installDejimad(ctx context.Context, distro string) error {
	ver := version.Version
	if ver == "" || ver == "dev" || !strings.HasPrefix(ver, "v") {
		var err error
		if ver, err = latestReleaseTag(ctx); err != nil {
			return err
		}
	}

	// ARCHITECTURE IS RESOLVED IN GO, from a one-word command, and the URL is
	// built here too.
	//
	// The first version did all of it in the shell — `arch=$(uname -m)`, a case
	// statement, and ${} interpolation into a URL. It failed on the operator's
	// machine with "unsupported architecture:" and NOTHING after the colon, so
	// the substitution came back empty or the case never matched. Which layer
	// mangled it (Windows argument quoting, wsl.exe's own re-parsing, a stray
	// CR) was not worth determining, because the fix for all of them is the
	// same: stop asking a fragile channel to carry logic it does not need to.
	//
	// `uname -m` is now the whole script. TrimSpace handles the CR that a
	// Windows-side round trip can leave on it — which is itself a candidate for
	// the original failure, since "x86_64\r" matches no case arm.
	rawArch, err := wsl.Run(ctx, distro, "uname -m")
	if err != nil {
		return fmt.Errorf("read architecture from %s: %w", distro, err)
	}
	arch := strings.TrimSpace(rawArch)
	var goarch string
	switch arch {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	case "":
		// Name what came back rather than reporting an empty architecture as
		// unsupported, which is what the first version did and which said
		// nothing about the cause.
		return fmt.Errorf("could not read the architecture of distro %q — `uname -m` returned nothing "+
			"(raw: %q). Try `wsl -d %s -- uname -m` by hand", distro, rawArch, distro)
	default:
		return fmt.Errorf("unsupported architecture %q in distro %q — Dejima publishes linux amd64 and arm64", arch, distro)
	}

	url := fmt.Sprintf("https://github.com/Scusi-Inc/dejima/releases/download/%s/dejima_%s_linux_%s.tar.gz", ver, ver, goarch)
	// The echo gets the BARE url so the operator sees a clickable address rather
	// than one wrapped in quotes; curl gets the quoted one.
	sumsURL := fmt.Sprintf("https://github.com/Scusi-Inc/dejima/releases/download/%s/SHA256SUMS", ver)
	asset := fmt.Sprintf("dejima_%s_linux_%s.tar.gz", ver, goarch)
	script := fmt.Sprintf(dejimadInstallScript,
		url, shellSingleQuote(url), asset,
		shellSingleQuote(sumsURL), shellSingleQuote(asset), asset, asset)
	if out, err := wsl.Run(ctx, distro, script); err != nil {
		return wslStepErr(out, err)
	}
	return nil
}

// latestReleaseTag resolves the newest published release. Only reached by a dev
// build, which has no matching release to pin to.
func latestReleaseTag(ctx context.Context) (string, error) {
	info, err := selfupdate.LatestReleaseInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve the latest release: %w", err)
	}
	if info.Tag == "" {
		return "", fmt.Errorf("the latest release has no version tag")
	}
	return info.Tag, nil
}

// buildIslandImage builds the island image inside the distro.
//
// This step used to come free: install.sh handed off to `make setup`, which runs
// `make image`. Dropping the source build drops that with it, and a distro with a
// daemon and no image fails on the operator's first `dejima init` — AFTER a
// setup that reported success, which is the worst place for it to surface.
//
// It needs no checkout: the daemon embeds the build context (islandimage
// .Materialize), so a release-installed dejimad can build its own image.
func buildIslandImage(ctx context.Context, distro string) error {
	out, err := wsl.Run(ctx, distro, `
		set -e
		if docker image inspect dejima/island >/dev/null 2>&1; then
			echo already-built; exit 0
		fi
		dejima image build`)
	return wslStepErr(out, err)
}

// startDaemonInWSL brings dejimad up in the background inside the distro and
// waits for its socket. It does NOT use `dejima service install`: WSL images
// commonly ship without systemd, so a nohup'd process plus this idempotent
// start is the arrangement that actually survives there. `dejima wsl setup` is
// cheap to re-run after a `wsl --shutdown`.
func startDaemonInWSL(ctx context.Context, distro string) error {
	// This function used to build every path from $HOME and count with $i. On a
	// real machine that produced
	//
	//	sh: 18: [: Illegal number:
	//
	// because $i arrived EMPTY — the same channel that had just eaten $work one
	// function over. I removed the variables from the install script and left
	// them here, so the operator's first run failed at the step after the one I
	// had finally fixed.
	//
	// So: no shell variables anywhere. Paths are read once and interpolated in
	// Go; the retry loop lives in Go, where a counter and a comparison are things
	// that can be tested.
	home, err := distroHome(ctx, distro)
	if err != nil {
		return err
	}
	sock := home + "/.dejima/dejimad.sock"
	logf := home + "/.dejima/dejimad.log"

	if _, err := wsl.Run(ctx, distro, "test -S "+sock+" && pgrep -x dejimad >/dev/null 2>&1"); err == nil {
		return nil // already up
	}
	// Clear a stale socket from an unclean shutdown; dejimad refuses to bind over
	// one. Only when no daemon is actually running.
	_, _ = wsl.Run(ctx, distro, "pgrep -x dejimad >/dev/null 2>&1 || rm -f "+sock)

	if _, err := wsl.Run(ctx, distro,
		"mkdir -p "+home+"/.dejima && nohup dejimad --foreground >>"+logf+" 2>&1 &"); err != nil {
		return fmt.Errorf("start dejimad in %s: %w", distro, err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := wsl.Run(ctx, distro, "test -S "+sock); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	// Hand back the daemon's own last words. "It never started" and "it started
	// and died" need different fixes and look identical from a timeout.
	tail, _ := wsl.Run(ctx, distro, "tail -n 20 "+logf)
	if strings.TrimSpace(tail) == "" {
		tail = "(the log is empty — dejimad may not have started at all)"
	}
	return fmt.Errorf("dejimad in %s did not create its socket within 60s. Last log lines:\n%s",
		distro, strings.TrimSpace(tail))
}

// distroHome reads the distro's home directory.
//
// `printenv HOME`, not `echo $HOME`: this channel eats `$`, which is why no
// script in this file contains one.
func distroHome(ctx context.Context, distro string) (string, error) {
	out, err := wsl.Run(ctx, distro, "printenv HOME")
	if err != nil {
		return "", fmt.Errorf("read HOME from %s: %w", distro, err)
	}
	home := strings.TrimSpace(out)
	if home == "" || !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("distro %q reported an unusable HOME (%q)", distro, out)
	}
	return home, nil
}

// saveWSLProfile persists the distro as a connection profile and makes it
// active. Re-running setup updates the existing entry in place rather than
// erroring on the duplicate name.
func saveWSLProfile(distro string) error {
	name := profileNameFor(distro)
	host := wsl.Host(distro)
	cfg, err := clientcfg.Load()
	if err != nil {
		return err
	}
	updated := false
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == name {
			cfg.Profiles[i].Host = host
			updated = true
			break
		}
	}
	if !updated {
		cfg.Profiles = append(cfg.Profiles, clientcfg.Profile{Name: name, Host: host})
	}
	cfg.ActiveProfile = name
	return clientcfg.Save(cfg)
}

// confirmWSL asks a y/N question, defaulting to no. A non-TTY answers no, so a
// piped invocation can't silently install things — use --yes for that.
func confirmWSL(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	answer := strings.ToLower(strings.TrimSpace(readSingleKey("")))
	return answer == "y" || answer == "yes"
}

// runWSLExe invokes wsl.exe with management arguments (not a distro command),
// returning its combined output for error classification.
func runWSLExe(ctx context.Context, args ...string) (string, error) {
	return wsl.RunExe(ctx, args...)
}

// dejimadInstallScript downloads the release tarball and installs both binaries.
// One raw literal so the dash guard checks all of it; $want_ver is supplied by
// the caller as a shell assignment prepended to this text.
// dejimadInstallScript has NO SHELL VARIABLES AT ALL. Only %s, for the URL.
//
// It had two — $work and $HOME — and on the operator's machine produced
//
//	mkdir: cannot create directory '': No such file or directory
//
// while running correctly under dash locally, and with $HOME confirmed set to
// /root inside that very distro. So something between Go's exec and the distro's
// sh expands `$` in the script text: `$work` becomes empty, the quotes survive,
// and mkdir receives ”. d4 had already found that wsl.exe mangles embedded
// double quotes; this is the same channel chewing something else.
//
// I am not going to keep characterising that channel. Every round of this has
// cost the operator a reinstall, and each fix so far has been a smaller guess
// than the last. Removing the class of thing that gets mangled ends it: no
// variables, no substitution, no expansion — a literal path and a literal URL,
// both decided in Go where they can be tested.
//
// /var/tmp, not $HOME and not /tmp: it exists on every distro, survives the idle
// shutdown that empties tmpfs, and needs no expansion to name.
const dejimadInstallScript = `
		set -e
		rm -rf /var/tmp/dejima-install
		mkdir -p /var/tmp/dejima-install
		echo "downloading %s"
		curl -fsSL %s -o /var/tmp/dejima-install/%s
		# Verify before installing, and note the SHAPE: no shell variables
		# anywhere. This channel eats the dollar sign, which is why architecture is
		# resolved
		# in Go above — the same constraint applies here, so the asset name is
		# interpolated and sha256sum -c does the comparison itself.
		#
		# It matters because the reason this stopped building from source is a link
		# that drops connections mid-transfer. The same link truncates a tarball,
		# and an unverified one installs a corrupt daemon that fails later and
		# elsewhere. A MISSING SHA256SUMS is tolerated (an older release may not
		# publish one); a MISMATCH is fatal.
		cd /var/tmp/dejima-install
		if curl -fsSL %s -o SHA256SUMS 2>/dev/null && grep %s SHA256SUMS > want.sha; then
			sha256sum -c want.sha
		else
			echo "warning: no published checksum for %s; skipping verification" >&2
		fi
		tar -xzf /var/tmp/dejima-install/%s -C /var/tmp/dejima-install
		# sudo -n, never bare sudo. This runs as a non-interactive child of the
		# setup command: a password prompt here has no terminal to appear on, and
		# the setup would hang with no output explaining why. Failing loudly beats
		# stalling silently.
		install -m 0755 /var/tmp/dejima-install/dejima /var/tmp/dejima-install/dejimad /usr/local/bin/ 2>/dev/null || sudo -n install -m 0755 /var/tmp/dejima-install/dejima /var/tmp/dejima-install/dejimad /usr/local/bin/
		rm -rf /var/tmp/dejima-install
		command -v dejimad >/dev/null 2>&1`

// shellSingleQuote quotes a value for POSIX sh. The version comes from our own
// build, not from user input, but a quoting bug here would corrupt a URL rather
// than fail loudly.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
