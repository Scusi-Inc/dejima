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

func installSocat(ctx context.Context, distro string) error {
	_, err := wsl.Run(ctx, distro, `
		set -e
		if command -v apt-get >/dev/null 2>&1; then
			sudo -n apt-get update -qq && sudo -n apt-get install -y -qq socat
		elif command -v dnf >/dev/null 2>&1; then
			sudo -n dnf install -y -q socat
		elif command -v apk >/dev/null 2>&1; then
			sudo -n apk add --quiet socat
		else
			echo "no supported package manager (need apt/dnf/apk)" >&2; exit 1
		fi`)
	return err
}

// installDocker runs Docker's official convenience script inside the distro and
// puts the user in the docker group. WSL2 has no systemd by default on older
// images, so we also make sure the daemon can be started by hand.
func installDocker(ctx context.Context, distro string, yes bool) error {
	if !yes && !confirmWSL("Install the Docker engine inside "+distro+" (get.docker.com)?") {
		return fmt.Errorf("cancelled — install Docker in the distro yourself, then re-run")
	}
	_, err := wsl.Run(ctx, distro, `
		set -e
		if ! command -v docker >/dev/null 2>&1; then
			curl -fsSL https://get.docker.com | sudo -n sh
		fi
		sudo -n usermod -aG docker "$(id -un)" || true
		# systemd is off in older WSL images; start dockerd directly if so.
		if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
			sudo -n systemctl enable --now docker
		else
			sudo -n service docker start || (sudo -n dockerd >/tmp/dockerd.log 2>&1 &)
			sleep 5
		fi
		docker info >/dev/null 2>&1`)
	if err != nil {
		return fmt.Errorf("%w\n(check inside the distro:  wsl -d %s -- docker info)", err, distro)
	}
	return nil
}

// installDejimad installs the daemon from the official install script, which
// also builds the island image.
func installDejimad(ctx context.Context, distro string) error {
	// DEJIMA_ROLE=server is not optional here. install.sh now asks SERVER or
	// CLIENT on /dev/tty, and it asks there precisely because a `curl … | bash`
	// pipe is not evidence that nobody is watching. This call is a non-interactive
	// child of `dejima wsl setup`: if a terminal is reachable from inside the
	// distro, the installer would stop and wait for an answer nobody is there to
	// give, and `wsl setup` would hang with no output explaining why.
	//
	// Whether wsl.exe hands the child a usable /dev/tty is not something to
	// discover in the field on someone's first install. Answer it here.
	_, err := wsl.Run(ctx, distro, `
		set -e
		curl -fsSL https://dejima.tech/install.sh | DEJIMA_ROLE=server SKIP_SERVICE=1 bash
		command -v dejimad >/dev/null 2>&1`)
	return err
}

// startDaemonInWSL brings dejimad up in the background inside the distro and
// waits for its socket. It does NOT use `dejima service install`: WSL images
// commonly ship without systemd, so a nohup'd process plus this idempotent
// start is the arrangement that actually survives there. `dejima wsl setup` is
// cheap to re-run after a `wsl --shutdown`.
func startDaemonInWSL(ctx context.Context, distro string) error {
	out, err := wsl.Run(ctx, distro, `
		set -e
		if [ -S "$HOME/.dejima/dejimad.sock" ] && pgrep -x dejimad >/dev/null 2>&1; then
			echo already-running; exit 0
		fi
		# Clear a stale socket from an unclean shutdown; dejimad refuses to bind over one.
		[ -S "$HOME/.dejima/dejimad.sock" ] && ! pgrep -x dejimad >/dev/null 2>&1 && rm -f "$HOME/.dejima/dejimad.sock"
		mkdir -p "$HOME/.dejima"
		nohup dejimad --foreground >>"$HOME/.dejima/dejimad.log" 2>&1 &
		# POSIX counter, deliberately not a seq expansion. wsl.Run executes this
		# through sh -c, which is DASH on Ubuntu, and a distro without coreutils
		# expands seq to nothing -- leaving an empty for-list, which dash rejects
		# with "Syntax error: word unexpected (expecting do)". That is our script
		# failing on the operator machine, and it fired AFTER everything else had
		# worked: a clean install path with one last landmine at the end.
		i=0
		while [ "$i" -lt 60 ]; do
			[ -S "$HOME/.dejima/dejimad.sock" ] && echo started && exit 0
			sleep 1
			i=$((i + 1))
		done
		echo "dejimad did not create its socket within 60s; last log lines:" >&2
		tail -n 20 "$HOME/.dejima/dejimad.log" >&2
		exit 1`)
	if err != nil {
		return fmt.Errorf("start dejimad in %s: %w", distro, err)
	}
	_ = out
	return nil
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
