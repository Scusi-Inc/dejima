package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/vmmem"
)

// This file is the `dejima onboard --provision-host` wizard: fresh Mac mini →
// working Dejima host in one command. It walks a fixed sequence of phases, each
// detect→act→verify, auto-doing whatever is scriptable and only *instructing* for
// GUI-only steps. Progress is persisted so an exit mid-flow (e.g. waiting for
// Docker Desktop to finish installing) resumes where it left off.
//
// macOS-only by design (see docs/host-provisioning-plan.md "Non-goals"); a Linux
// equivalent is a separate effort. On any other OS this prints a pointer to the
// generic `dejima onboard` and stops.

// provState is the resumable wizard progress at ~/.dejima/provisioning-state.json.
type provState struct {
	StartedAt       time.Time         `json:"started_at"`
	CompletedPhases []string          `json:"completed_phases"`
	Skipped         []string          `json:"skipped,omitempty"`
	Answers         map[string]string `json:"answers,omitempty"`
}

func (s *provState) done(id string) bool {
	for _, p := range s.CompletedPhases {
		if p == id {
			return true
		}
	}
	return false
}

func (s *provState) markDone(id string) {
	if !s.done(id) {
		s.CompletedPhases = append(s.CompletedPhases, id)
	}
}

func (s *provState) markSkipped(id string) {
	for _, p := range s.Skipped {
		if p == id {
			return
		}
	}
	s.Skipped = append(s.Skipped, id)
}

func provStatePath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "provisioning-state.json"), nil
}

func loadProvState() *provState {
	st := &provState{Answers: map[string]string{}}
	p, err := provStatePath()
	if err != nil {
		return st
	}
	if data, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(data, st)
		if st.Answers == nil {
			st.Answers = map[string]string{}
		}
	}
	if st.StartedAt.IsZero() {
		st.StartedAt = time.Now().UTC()
	}
	return st
}

func saveProvState(st *provState) {
	p, err := provStatePath()
	if err != nil {
		return
	}
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(p, data, 0o600)
	}
}

func resetProvState() {
	if p, err := provStatePath(); err == nil {
		_ = os.Remove(p)
	}
}

// provCtx threads the wizard's shared state through the phases.
type provCtx struct {
	ctx    context.Context
	yes    bool // non-interactive: auto-confirm scriptable steps, skip GUI pauses
	state  *provState
	env    *envProbe
	manual []string // GUI-only / off-box steps to print at the end
}

// addManual records a step the wizard couldn't do for the user, surfaced as a
// checklist at the end (the only output a --yes run produces for GUI steps).
func (pc *provCtx) addManual(s string) { pc.manual = append(pc.manual, s) }

// confirm asks a yes/no question, defaulting to defYes. Under --yes it returns
// defYes without prompting, so a non-interactive run never blocks — but a step
// that should never be auto-taken passes defYes=false.
func (pc *provCtx) confirm(prompt string, defYes bool) bool {
	if pc.yes {
		return defYes
	}
	suffix := "[y/N]"
	if defYes {
		suffix = "[Y/n]"
	}
	ans := strings.TrimSpace(readSingleKey(prompt + " " + suffix + ": "))
	if ans == "" {
		return defYes
	}
	return strings.EqualFold(ans, "y")
}

type provPhase struct {
	id    string
	title string
	run   func(pc *provCtx) error
}

// runProvisionHost is the entry point for `dejima onboard --provision-host`.
func runProvisionHost(ctx context.Context, yes, reset bool) error {
	if runtime.GOOS != "darwin" {
		fmt.Println("`--provision-host` provisions a macOS host (the Mac-mini-as-agent-server path).")
		fmt.Printf("This machine is %s — run `dejima onboard` for the generic, cross-platform setup.\n", runtime.GOOS)
		return nil
	}
	if reset {
		resetProvState()
		fmt.Println("Cleared saved provisioning progress — starting fresh.")
	}

	st := loadProvState()
	pc := &provCtx{ctx: ctx, yes: yes, state: st, env: detectEnv()}

	fmt.Println()
	fmt.Println(bold("Dejima host provisioning — turn this Mac into a personal AI-agent server"))
	fmt.Println()
	fmt.Println("I'll walk this machine through the steps to become a secure, always-on Dejima")
	fmt.Println("host: never-sleep power settings, the tooling (Homebrew/Tailscale/Docker), and")
	fmt.Println("the daemon itself. I auto-do what I can and tell you what needs a click.")
	if yes {
		fmt.Println("Running with --yes: scriptable steps proceed automatically; GUI-only steps are")
		fmt.Println("collected into a checklist at the end.")
	}
	fmt.Println()
	if v := macOSVersion(); v != "" {
		fmt.Printf("macOS %s · host %s\n\n", v, hostName())
	}

	phases := []provPhase{
		{"system-config", "Power & system settings", provPhaseSystemConfig},
		{"tooling", "Tooling — Homebrew, Tailscale, Docker", provPhaseTooling},
		{"vm-rightsize", "Docker VM memory", provPhaseVMRightsize},
		{"shell-ssh", "Shell PATH & Remote Login", provPhaseShellSSH},
		{"dejima-install", "Install the Dejima daemon", provPhaseDejimaInstall},
		{"verify", "Verify & connection info", provPhaseVerify},
	}

	for i, ph := range phases {
		fmt.Printf("%s %s\n", bold(fmt.Sprintf("[%d/%d]", i+1, len(phases))), bold(ph.title))
		if st.done(ph.id) {
			fmt.Printf("  ✓ already done (skipping; --reset to redo from scratch)\n\n")
			continue
		}
		if err := ph.run(pc); err != nil {
			// A phase error is not fatal to the whole wizard: save progress and let
			// the user re-run to resume. We stop here so they can fix the blocker.
			saveProvState(st)
			fmt.Printf("\n  ✗ %s: %v\n", ph.id, err)
			fmt.Println("  Fix the above, then re-run `dejima onboard --provision-host` to resume.")
			return nil
		}
		st.markDone(ph.id)
		saveProvState(st)
		fmt.Println()
	}

	printProvManual(pc)
	// The authoritative "did it actually work?" verdict comes from the caller's
	// markSetupDoneIfHealthy, which checks the daemon answers. Don't print an
	// unconditional success banner here that could contradict it.
	fmt.Println(bold("Provisioning steps complete — verifying the daemon…"))
	return nil
}

// ---------------------------------------------------------------------------
// Phase 1 — power & system settings (never sleep is the critical one)
// ---------------------------------------------------------------------------

func provPhaseSystemConfig(pc *provCtx) error {
	// A sleeping Mac mini drops every attached session and stops every agent —
	// the single most important host setting. pmset is scriptable (with sudo).
	pm := pmsetValues()
	needsSleep := pm["sleep"] != "0"
	needsWomp := pm["womp"] != "1"           // wake for network access
	needsRestart := pm["autorestart"] != "1" // restart after a power failure

	if !needsSleep && !needsWomp && !needsRestart {
		fmt.Println("  ✓ already never-sleeps, wakes on network, and restarts after power loss")
	} else {
		fmt.Println("  A Mac mini server must never sleep (sleep drops every session + stops agents).")
		fmt.Println("  I'll set: sleep off, wake-on-network on, auto-restart-after-power-failure on.")
		fmt.Println("  This needs sudo (you may be asked for your password).")
		if pc.confirm("  Apply never-sleep power settings now?", true) {
			// disablesleep guards against any idle sleep; sleep 0 disables system
			// sleep; womp wakes for network; autorestart recovers from power loss.
			if err := execInteractive("sudo", "pmset", "-a",
				"sleep", "0", "disablesleep", "1", "womp", "1", "autorestart", "1"); err != nil {
				fmt.Printf("  ✗ pmset failed: %v\n", err)
				pc.addManual("Disable sleep: sudo pmset -a sleep 0 disablesleep 1 womp 1 autorestart 1")
			} else if after := pmsetValues(); after["sleep"] == "0" {
				fmt.Println("  ✓ power settings applied (verified: sleep is off)")
			} else {
				fmt.Println("  ⚠ pmset ran but sleep still isn't 0 — check `pmset -g`")
			}
		} else {
			pc.addManual("Disable sleep: sudo pmset -a sleep 0 disablesleep 1 womp 1 autorestart 1")
		}
	}

	// Auto-login lets LaunchAgents come back after an unattended reboot. It can't
	// be flipped safely from the CLI (it stores the account password), so this is
	// always a GUI instruction.
	fmt.Println()
	fmt.Println("  Auto-login (so the daemon returns after a reboot with no one at the keyboard):")
	fmt.Println("    System Settings → Users & Groups → Automatically log in as → <your user>")
	fmt.Println("    (Optional if you installed the daemon as a --system LaunchDaemon, which")
	fmt.Println("     starts at boot before any login.)")
	pc.addManual("Enable auto-login: System Settings → Users & Groups → Automatically log in as")
	return nil
}

// pmsetValues parses `pmset -g` into a flat map of setting→value.
func pmsetValues() map[string]string {
	out := map[string]string{}
	b, err := exec.Command("pmset", "-g").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Phase 2 — tooling: Homebrew, Tailscale, Docker (gh is optional)
// ---------------------------------------------------------------------------

func provPhaseTooling(pc *provCtx) error {
	// Homebrew first — everything else installs through it.
	if _, err := exec.LookPath("brew"); err != nil && !brewOnDisk() {
		fmt.Println("  Homebrew isn't installed (the package manager for everything below).")
		if pc.confirm("  Install Homebrew now?", true) {
			script := `/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`
			cmd := exec.Command("/bin/bash", "-c", script)
			if pc.yes {
				cmd.Env = append(os.Environ(), "NONINTERACTIVE=1")
			}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Printf("  ✗ Homebrew install failed: %v\n", err)
				pc.addManual("Install Homebrew: " + script)
			}
		} else {
			pc.addManual("Install Homebrew (then re-run): see https://brew.sh")
		}
	} else {
		fmt.Println("  ✓ Homebrew present")
	}
	// Ensure brew is on PATH for the rest of THIS process so subsequent installs
	// resolve even on a brand-new install (the shell rc edit only affects new shells).
	ensureBrewOnPath()

	brewAvail := false
	if _, err := exec.LookPath("brew"); err == nil {
		brewAvail = true
	}

	// Hard gate: Homebrew is the package manager every later install uses. If it
	// isn't available (the install above failed or was declined), stop HERE with
	// the root cause — rather than cascading into confusing "command not found"
	// failures when we brew-install Tailscale/Docker below and in later phases.
	// Resumable: fix Homebrew, then re-run to pick up from this phase.
	if !brewAvail {
		return fmt.Errorf("need Homebrew for the rest of setup, but it isn't available — " +
			"the install above failed or was declined. Install it, then re-run:\n" +
			`      /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`)
	}

	// Tailscale — the remote-access substrate (and how other devices reach the host).
	if _, err := exec.LookPath("tailscale"); err != nil {
		fmt.Println()
		fmt.Println("  Tailscale isn't installed (the private network other devices use to reach this host).")
		if brewAvail && pc.confirm("  Install Tailscale now?", true) {
			if err := execInteractive("brew", "install", "--cask", "tailscale"); err != nil {
				fmt.Printf("  ✗ install failed: %v\n", err)
				pc.addManual("Install Tailscale: brew install --cask tailscale (or https://tailscale.com/download)")
			}
		} else if !brewAvail {
			pc.addManual("Install Tailscale: brew install --cask tailscale (after Homebrew is in place)")
		}
	} else {
		fmt.Println("  ✓ Tailscale present")
	}
	// Bring the tailnet up with the SSH server on (idempotent; opens a browser the
	// first time). Off-box account login is inherently interactive, so skip under --yes.
	if _, err := exec.LookPath("tailscale"); err == nil {
		if st := tailscaleStatus(); st.BackendState == "Running" {
			fmt.Println("  ✓ Tailscale is up")
			if fqdn := tailnetFQDN(); fqdn != "" {
				pc.state.Answers["tailnet_fqdn"] = fqdn
			}
		} else if pc.yes {
			pc.addManual("Bring Tailscale up: tailscale up --ssh --accept-dns=true")
		} else if pc.confirm("  Bring Tailscale up now (opens a browser to log in)?", true) {
			if err := execInteractive("tailscale", "up", "--ssh", "--accept-dns=true"); err != nil {
				fmt.Printf("  ✗ `tailscale up` failed: %v\n", err)
				pc.addManual("Bring Tailscale up: tailscale up --ssh --accept-dns=true")
			} else if fqdn := tailnetFQDN(); fqdn != "" {
				pc.state.Answers["tailnet_fqdn"] = fqdn
				fmt.Printf("  ✓ on the tailnet as %s\n", fqdn)
			}
		}
	}

	// Docker — the container engine islands run in.
	fmt.Println()
	if dockerReachable() {
		fmt.Println("  ✓ Docker is reachable")
	} else {
		fmt.Println("  Docker isn't reachable (the container engine that runs islands).")
		if _, err := exec.LookPath("docker"); err != nil && brewAvail {
			if pc.confirm("  Install Docker Desktop now?", true) {
				if err := execInteractive("brew", "install", "--cask", "docker"); err != nil {
					fmt.Printf("  ✗ install failed: %v\n", err)
					pc.addManual("Install Docker Desktop: brew install --cask docker")
				}
			}
		}
		// Docker Desktop needs a one-time GUI launch to grant permissions and start
		// its VM — not scriptable. Instruct, then poll briefly so a quick launch is
		// picked up without re-running the wizard.
		fmt.Println("  Launch Docker Desktop once (/Applications/Docker.app) to grant permissions")
		fmt.Println("  and start its engine.")
		if !pc.yes && pc.confirm("  I've launched it — wait for the engine to come up?", true) {
			if waitForDocker(20) {
				fmt.Println("  ✓ Docker engine is up")
			} else {
				fmt.Println("  ⚠ still not reachable — finish the Docker Desktop first-launch, then re-run")
				pc.addManual("Start Docker Desktop and confirm `docker version` reaches a server")
			}
		} else if !dockerReachable() {
			pc.addManual("Start Docker Desktop and confirm `docker version` reaches a server")
		}
	}

	// gh is a convenience (per-island GitHub identities), not required to stand up
	// the host — offer it but never block on it.
	if _, err := exec.LookPath("gh"); err != nil && brewAvail {
		fmt.Println()
		if pc.confirm("  Install the GitHub CLI (gh) for repo access? (optional)", false) {
			if err := execInteractive("brew", "install", "gh"); err != nil {
				fmt.Printf("  ✗ install failed: %v\n", err)
			} else {
				pc.addManual("Authenticate gh when ready: gh auth login")
			}
		}
	}
	pc.env = detectEnv() // re-probe; later phases depend on docker/tailscale state

	// Hard gate: islands need a reachable container engine. Stop at the source if
	// Docker still isn't up, rather than letting it resurface as a disconnected
	// "docker not reachable" in the VM-sizing and daemon-install phases.
	if !dockerReachable() {
		return fmt.Errorf("the Docker engine isn't reachable yet — finish Docker Desktop's first launch " +
			"(open /Applications/Docker.app and wait for its engine), then re-run. " +
			"on a headless Mac, use colima instead: `brew install colima docker && colima start`")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Phase 3 — Docker VM memory right-size (#23 substrate fix)
// ---------------------------------------------------------------------------

func provPhaseVMRightsize(pc *provCtx) error {
	if !dockerReachable() {
		fmt.Println("  (Docker not reachable yet — skipping the VM-memory check; re-run after Docker is up.)")
		pc.state.markSkipped("vm-rightsize")
		return nil
	}
	host := vmmem.HostMemoryBytes()
	vm := dockerVMMemoryBytes()
	if host == 0 || vm == 0 {
		fmt.Println("  (Couldn't read host/VM memory — skipping.)")
		pc.state.markSkipped("vm-rightsize")
		return nil
	}
	if !vmmem.Undersized(host, vm) {
		fmt.Printf("  ✓ Docker VM has %s of %s host RAM — fine\n", humanBytes(vm), humanBytes(host))
		return nil
	}
	fmt.Printf("  ⚠ Docker VM has only %s of %s host RAM — islands share this pool and will OOM.\n",
		humanBytes(vm), humanBytes(host))
	recGB := vmmem.RecommendedGB(host)

	// colima can resize from the CLI, so do it inline — suggest the ~⅔ default but
	// let the user confirm or override the number. Docker Desktop has no CLI resize
	// (its memory is a GUI slider), so that path keeps the doctor/checklist hint.
	if vmmem.ColimaAvailable() {
		fmt.Printf("    Recommended: ~%dGB (≈⅔ of host). colima can apply this directly.\n", recGB)
		gb := pc.promptMemoryGB(recGB)

		// A resize starts colima with the new size; on an already-running VM that
		// restarts it, bouncing every island. Warn + default-NO before that, but
		// still proceed under --yes (the scriptable path) with a clear log line.
		if colimaRunning() {
			fmt.Println("  ⚠ colima is already running — applying a new memory size RESTARTS the VM")
			fmt.Println("    and BOUNCES all islands (every running container restarts).")
			if pc.yes {
				fmt.Println("  --yes: proceeding with the resize (this bounces running islands).")
			} else if !pc.confirm(fmt.Sprintf("  Resize to %dGB now and bounce running islands?", gb), false) {
				pc.addManual(fmt.Sprintf("Resize the Docker VM to %dGB when islands are idle: colima start --memory %d", gb, gb))
				return nil
			}
		}

		// Omit --cpu/--disk so colima keeps its saved values for those.
		if err := execInteractive("colima", "start", "--memory", strconv.Itoa(gb)); err != nil {
			fmt.Printf("  ✗ colima start --memory %d: %v\n", gb, err)
			pc.addManual(fmt.Sprintf("Right-size the Docker VM: colima start --memory %d", gb))
			return nil
		}
		fmt.Printf("  ✓ ran: colima start --memory %d\n", gb)
		return nil
	}

	// Docker Desktop — no CLI resize; point at doctor --fix / the GUI slider.
	fmt.Printf("    Recommended: ~%dGB. `dejima doctor --fix` scripts the colima resize.\n", recGB)
	if pc.confirm("  Run `dejima doctor --fix` now?", true) {
		if self, err := os.Executable(); err == nil {
			if err := execInteractive(self, "doctor", "--fix"); err != nil {
				fmt.Printf("  ✗ doctor --fix: %v\n", err)
				pc.addManual(fmt.Sprintf("Right-size the Docker VM (Docker Desktop → Settings → Resources, or colima start --memory %d)", recGB))
			}
		}
	} else {
		pc.addManual(fmt.Sprintf("Right-size the Docker VM to ~%dGB (Docker Desktop → Settings → Resources)", recGB))
	}
	return nil
}

// promptMemoryGB asks the user to confirm the recommended VM memory size (in GB)
// or override it with a custom figure. An empty answer takes the default; a
// positive integer takes that value; garbage re-prompts once then falls back to
// the default. Under --yes it returns the default without prompting.
func (pc *provCtx) promptMemoryGB(def int) int {
	if pc.yes {
		return def
	}
	prompt := fmt.Sprintf("  Docker VM memory in GB [%d]: ", def)
	if gb, ok := parseMemoryGB(readSingleKey(prompt), def); ok {
		return gb
	}
	fmt.Println("  (not a positive whole number — press Enter for the default, or type a GB value)")
	gb, _ := parseMemoryGB(readSingleKey(prompt), def)
	return gb
}

// parseMemoryGB interprets one answer to the memory-size prompt. Empty means
// "take the default"; a positive integer means that many GB; anything else is
// garbage — it returns the default with ok=false so the caller can re-prompt.
func parseMemoryGB(answer string, def int) (gb int, ok bool) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return def, true
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n <= 0 {
		return def, false
	}
	return n, true
}

// colimaRunning reports whether the colima VM is currently up. `colima status`
// exits 0 only when the VM is running, so the exit code is the signal (matching
// how dockerReachable shells out on `docker version`).
func colimaRunning() bool {
	return exec.Command("colima", "status").Run() == nil
}

// ---------------------------------------------------------------------------
// Phase 4 — shell PATH + Remote Login
// ---------------------------------------------------------------------------

func provPhaseShellSSH(pc *provCtx) error {
	// Non-interactive SSH sessions don't source an interactive shell, so brew
	// binaries (and `dejima`) must be on PATH via ~/.zshenv for the SSH-façade and
	// remote `dejima` to find them.
	if home, err := os.UserHomeDir(); err == nil {
		zshenv := filepath.Join(home, ".zshenv")
		line := `export PATH="/opt/homebrew/bin:$PATH"`
		if err := appendLineIfAbsent(zshenv, line); err != nil {
			fmt.Printf("  ⚠ couldn't update %s: %v\n", tildeify(zshenv), err)
		} else {
			fmt.Printf("  ✓ %s has /opt/homebrew/bin on PATH (for non-interactive SSH)\n", tildeify(zshenv))
		}
	}

	// Remote Login (SSH) — needed for the VS Code / Cursor Remote-SSH on-ramp and
	// sftp. Scriptable with sudo.
	if remoteLoginOn() {
		fmt.Println("  ✓ Remote Login (SSH) is on")
	} else {
		fmt.Println("  Remote Login (SSH) is off — needed for VS Code/Cursor Remote-SSH into islands.")
		if pc.confirm("  Enable Remote Login now (sudo)?", true) {
			if err := execInteractive("sudo", "systemsetup", "-setremotelogin", "on"); err != nil {
				fmt.Printf("  ✗ couldn't enable Remote Login: %v\n", err)
				pc.addManual("Enable Remote Login: System Settings → General → Sharing → Remote Login")
			} else {
				fmt.Println("  ✓ Remote Login enabled")
			}
		} else {
			pc.addManual("Enable Remote Login: System Settings → General → Sharing → Remote Login")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Phase 5 — install the Dejima daemon as a boot service
// ---------------------------------------------------------------------------

func provPhaseDejimaInstall(pc *provCtx) error {
	if !dockerReachable() {
		fmt.Println("  Docker isn't reachable yet — the daemon needs it. Finish the Docker step, then re-run.")
		return fmt.Errorf("docker not reachable")
	}
	self, _ := os.Executable()

	if _, err := exec.LookPath("dejimad"); err != nil {
		// No daemon binary yet. The headline path is `go install …/dejima@latest`
		// (client only), so building the server from source is the missing piece.
		fmt.Println("  The daemon binary (dejimad) isn't installed yet.")
		if _, statErr := os.Stat("Makefile"); statErr == nil {
			fmt.Println("  A source checkout is here — `make setup` builds + installs the full stack.")
			if pc.confirm("  Run `make setup` now?", true) {
				if err := execInteractive("make", "setup"); err != nil {
					fmt.Printf("  ✗ make setup: %v\n", err)
					return fmt.Errorf("make setup failed")
				}
			}
		} else {
			fmt.Println("  Install the server from source, then re-run this wizard to finish:")
			fmt.Println(indentBlock("git clone https://github.com/aoos/dejima.git ~/code/dejima\ncd ~/code/dejima && make setup", "    "))
			pc.addManual("Install the Dejima server: git clone …/dejima && make setup")
			return nil
		}
	}

	// Install (or reinstall) dejimad as a boot LaunchDaemon with the recommended
	// host posture: tailnet TCP for remote clients, the in-island autonomy path,
	// and the operational audit log on.
	fmt.Println("  Installing dejimad as a system service (starts at boot, no login needed) with:")
	fmt.Println("    • remote access on :7273 (tailnet peers only)")
	fmt.Println("    • in-island autonomy on 127.0.0.1:7274")
	fmt.Println("    • the operational audit log (--audit)")
	if pc.confirm("  Install the service now (sudo)?", true) {
		args := []string{"service", "install", "--system",
			"--tcp", ":7273", "--token-tcp", "127.0.0.1:7274", "--audit",
			"--no-tcp-prompt", "--no-notify-prompt"}
		if err := execInteractive(self, args...); err != nil {
			fmt.Printf("  ✗ service install: %v\n", err)
			pc.addManual("Install the daemon: dejima service install --system --tcp :7273 --token-tcp 127.0.0.1:7274 --audit")
			return nil
		}
		fmt.Println("  ✓ daemon installed and supervised")
	} else {
		pc.addManual("Install the daemon: dejima service install --system --tcp :7273 --token-tcp 127.0.0.1:7274 --audit")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Phase 6 — verify + handoff
// ---------------------------------------------------------------------------

func provPhaseVerify(pc *provCtx) error {
	if self, err := os.Executable(); err == nil {
		fmt.Println("  Running `dejima doctor` …")
		_ = execInteractive(self, "doctor")
	}
	fqdn := tailnetFQDN()
	if fqdn == "" {
		fqdn = pc.state.Answers["tailnet_fqdn"]
	}
	user := currentUnixUser()

	fmt.Println()
	fmt.Println(bold("  This host is ready. From your other devices:"))
	fmt.Println()
	if fqdn != "" {
		fmt.Printf("    This host's Tailscale name: %s\n\n", fqdn)
		fmt.Println("    On a laptop (same Tailscale account):")
		fmt.Println("      go install github.com/aoos/dejima/cmd/dejima@latest")
		fmt.Printf("      export DEJIMA_HOST=%s:7273\n", fqdn)
		fmt.Println("      dejima ls")
		if user != "" {
			fmt.Printf("    Or open a shell on the host:  ssh %s@%s\n", user, fqdn)
		}
	} else {
		fmt.Println("    (Bring Tailscale up — `tailscale up --ssh` — to get this host's name for")
		fmt.Println("     remote access, then: export DEJIMA_HOST=<host>:7273 && dejima ls)")
	}
	maybeWriteCheatsheet(pc, fqdn, user)
	return nil
}

// printProvManual prints the accumulated GUI-only / off-box checklist.
func printProvManual(pc *provCtx) {
	if len(pc.manual) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(bold("Still to do by hand (the steps I can't automate):"))
	for _, m := range pc.manual {
		fmt.Printf("  • %s\n", m)
	}
	fmt.Println()
}

// maybeWriteCheatsheet drops a one-page connection reference on the Desktop.
func maybeWriteCheatsheet(pc *provCtx, fqdn, user string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	desktop := filepath.Join(home, "Desktop")
	if _, err := os.Stat(desktop); err != nil {
		return // no Desktop (headless) — skip silently
	}
	host := fqdn
	if host == "" {
		host = "<this-host>.tailnet"
	}
	if user == "" {
		user = "<you>"
	}
	body := fmt.Sprintf(`Dejima — quick reference
========================

This host:        %s
Connect from another device (same Tailscale account):

  go install github.com/aoos/dejima/cmd/dejima@latest
  export DEJIMA_HOST=%s:7273
  dejima ls

Open a shell on the host:
  ssh %s@%s

On the host:
  dejima              # dashboard
  dejima ls           # islands
  dejima doctor       # health check
  dejima audit        # the audit ledger
`, host, host, user, host)
	p := filepath.Join(desktop, "dejima-quick-reference.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err == nil {
		fmt.Printf("\n  Wrote a connection cheatsheet to %s\n", tildeify(p))
	}
}

// ---------------------------------------------------------------------------
// small probes
// ---------------------------------------------------------------------------

func brewOnDisk() bool {
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// ensureBrewOnPath prepends Homebrew's bin to this process's PATH if present but
// not already resolvable, so a just-installed brew (and tools installed through
// it) are usable for the rest of the wizard without a new shell.
func ensureBrewOnPath() {
	if _, err := exec.LookPath("brew"); err == nil {
		return
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if _, err := os.Stat(filepath.Join(dir, "brew")); err == nil {
			os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return
		}
	}
}

func dockerReachable() bool {
	return exec.Command("docker", "version").Run() == nil
}

// waitForDocker polls `docker version` up to n times (2s apart) for the engine
// to come up after a Docker Desktop first-launch.
func waitForDocker(n int) bool {
	for i := 0; i < n; i++ {
		if dockerReachable() {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

func dockerVMMemoryBytes() uint64 {
	out, err := exec.Command("docker", "info", "--format", "{{.MemTotal}}").Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	return v
}

func remoteLoginOn() bool {
	// `systemsetup -getremotelogin` prints "Remote Login: On" / "Off". It needs
	// root on some macOS versions; if it errors we report off (the act step is
	// idempotent, so a re-enable is harmless).
	out, err := exec.Command("systemsetup", "-getremotelogin").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "on")
}

func macOSVersion() string {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func hostName() string {
	if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	h, _ := os.Hostname()
	return h
}
