package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
	manual []manualStep // GUI-only / off-box steps to print at the end

	// incomplete marks phases that RAN but did not achieve their goal, so the
	// runner does not record them as done. Without this the only two outcomes
	// were "done" and "abort the wizard", and an optional phase can be neither:
	// local models failed to install, returned nil so the wizard would carry on,
	// and was then marked complete — so the reinstall an operator does to fix it
	// silently skips the very phase they reinstalled for. Declining is NOT
	// incomplete; that is an answer, and re-asking every run is nagging.
	incomplete map[string]bool
}

// markIncomplete records that this phase must run again next time.
func (pc *provCtx) markIncomplete(id string) {
	if pc.incomplete == nil {
		pc.incomplete = map[string]bool{}
	}
	pc.incomplete[id] = true
}

// manualStep is one thing the wizard could not do for the operator.
//
// Split into title/detail/why because the flat version of this list was the last
// thing a new operator read and it was a wall: a dozen bullets, each a sentence
// with a command buried in it, and nothing saying which of them actually
// mattered. The field report was "That's a lot of steps" — and most of them were
// optional. A checklist that does not distinguish "your install is incomplete"
// from "you could tune this later" gets read as all-required or all-ignorable,
// and both readings are wrong.
type manualStep struct {
	why    string // grouping header; "" files under Optional
	title  string // one line, imperative — what to do
	detail string // the command, or the click path
}

// Why-groups, ordered by how much they matter. Blocking first, taste last.
//
// There is no "performance, not urgent" tier any more. Right-sizing the Docker
// VM was in one, and the operator's correction was that it is worth doing BEFORE
// the host is in use rather than tuned afterwards — the whole point of a
// dedicated machine. Auto-login and Remote Login got the same correction. If a
// step is worth printing at all it is worth doing; the only genuinely
// take-it-or-leave-it category left is things Dejima can do later from its own
// UI, and those should not be in this list at all.
const (
	whyBlocking = "Dejima is NOT usable until these are done"
	whyRemote   = "To reach this Mac from your other devices"
	whyHost     = "To finish setting this Mac up as a server"
)

// guidedStep is one thing the wizard cannot do itself but CAN check.
//
// The old model collected every such step into a checklist at the end, which is
// the wizard giving up: it stated an action, never confirmed it, and left the
// operator holding a dozen equal-weight bullets with no way to tell which had
// taken effect. The field report was "That's a lot of steps ... the process just
// isn't buttoned up", and that is exactly right — a list is not a process.
//
// A step that can be VERIFIED should be walked, not listed: say what and why,
// wait, then check. The end-of-run list becomes the exception (skipped, or
// non-interactive) rather than the norm.
type guidedStep struct {
	why    string
	title  string
	detail string      // the command, or the click path; may be multi-line
	verify func() bool // nil = cannot be checked, so it can only be listed
	done   string      // what to say once verify passes
	notYet string      // what to say when it still isn't detected
}

// guide walks one step: skip it if already satisfied, otherwise explain, wait,
// and re-check until the operator is done or chooses to skip.
func (pc *provCtx) guide(g guidedStep) {
	if g.verify != nil && g.verify() {
		fmt.Printf("  ✓ %s\n", g.done)
		return
	}
	// Nobody to walk: --yes, or a step we cannot confirm either way. Fall back to
	// the checklist, which is what it is for.
	if pc.yes || g.verify == nil {
		pc.addManualFor(g.why, g.title, g.detail)
		return
	}

	fmt.Println()
	fmt.Printf("  %s\n", bold(g.title))
	fmt.Printf("    %s\n", g.why)
	for _, line := range strings.Split(g.detail, "\n") {
		if strings.TrimSpace(line) != "" {
			fmt.Printf("      %s\n", line)
		}
	}
	for {
		ans := strings.TrimSpace(readSingleKey("    Enter when done · [s]kip: "))
		if strings.EqualFold(ans, "s") {
			// Skipped deliberately — record it so the end-of-run list is the set
			// of things the operator CHOSE to leave, not a pile of unknowns.
			pc.addManualFor(g.why, g.title, g.detail)
			fmt.Println("    skipped — it'll be in the list at the end")
			return
		}
		if g.verify() {
			fmt.Printf("    ✓ %s\n", g.done)
			return
		}
		fmt.Printf("    ✗ %s\n", g.notYet)
	}
}

// autoLoginUser returns the account macOS is configured to log in automatically,
// or "" if auto-login is off. The plist is world-readable, so this needs no sudo
// — which matters, because a check that needs a password is a check that gets
// skipped.
func autoLoginUser() string {
	out, err := exec.Command("defaults", "read",
		"/Library/Preferences/com.apple.loginwindow", "autoLoginUser").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fileVaultOn reports whether FileVault is enabled. `fdesetup status` prints
// "FileVault is On." / "FileVault is Off." and — like autoLoginUser — needs no
// sudo, so the check can't be the thing that gets skipped.
func fileVaultOn() bool {
	out, err := exec.Command("fdesetup", "status").Output()
	if err != nil {
		return false // not macOS, or fdesetup missing: don't invent a constraint
	}
	return strings.Contains(string(out), "FileVault is On")
}

// autoLoginStep returns the "come back after a reboot" step for this Mac.
// FileVault is a parameter rather than a lookup inside, because it changes the
// answer completely and that has to be assertable in a test.
//
// macOS DISABLES the "Automatically log in as" control while FileVault is on, so
// the un-branched version walked the operator to a greyed-out setting and then
// told them, in a loop, that auto-login "still reads as off" — reported from the
// field as exactly that. The deeper promise fails too: a FileVault Mac stops at
// the unlock screen at boot, BEFORE any LaunchDaemon runs, so it cannot return
// from a power cut unattended whatever auto-login or `pmset autorestart` say.
// That is a decision for the operator, not a setting to poll, so it carries no
// verify: it belongs on the end-of-run list, not in a walk loop.
func autoLoginStep(fileVault bool) guidedStep {
	if fileVault {
		return guidedStep{
			why:   whyHost,
			title: "Decide how this Mac comes back after a reboot (FileVault is on)",
			detail: "macOS greys out auto-login while FileVault is on, and a FileVault Mac\n" +
				"stops at the unlock screen at boot — before any daemon starts.\n" +
				"Unattended recovery (what a dedicated host wants):\n" +
				"  sudo fdesetup disable\n" +
				"  then System Settings → Users & Groups → Automatically log in as\n" +
				"Keeping FileVault: someone unlocks at the screen after every boot.\n" +
				"  For a PLANNED reboot, `sudo fdesetup authrestart` skips that prompt.",
		}
	}
	return guidedStep{
		why:    whyHost,
		title:  "Enable auto-login",
		detail: "System Settings → Users & Groups → Automatically log in as → this account",
		verify: func() bool { return autoLoginUser() != "" },
		done:   "auto-login is on — this Mac comes back by itself after a reboot",
		notYet: "auto-login still reads as off (macOS may need the panel closed first)",
	}
}

// addManual records an OPTIONAL step. Prefer addManualFor when the step has a
// consequence worth naming; the default here is deliberately the weakest claim.
func (pc *provCtx) addManual(title, detail string) {
	pc.manual = append(pc.manual, manualStep{title: title, detail: detail})
}

// addManualFor records a step under a why-group.
func (pc *provCtx) addManualFor(why, title, detail string) {
	pc.manual = append(pc.manual, manualStep{why: why, title: title, detail: detail})
}

// confirm asks a yes/no question, defaulting to defYes. Under --yes it returns
// defYes without prompting, so a non-interactive run never blocks — but a step
// that should never be auto-taken passes defYes=false.
func (pc *provCtx) confirm(prompt string, defYes bool) bool {
	return pc.confirmUnattended(prompt, defYes, defYes)
}

// confirmUnattended splits the two defaults `confirm` conflates: what an
// interactive prompt should RECOMMEND, and what a --yes run should DO when
// nobody is asked. Those are not always the same answer.
//
// Local models forced the split. It is worth recommending to a person — running
// open-weights models on the host is much of the point of this machine — but it
// must never happen unattended, because it is a multi-GB download. Tying both
// to one flag meant the only way to keep `--yes` from pulling gigabytes was to
// show a person `[y/N]`, which reads as "we advise against this".
func (pc *provCtx) confirmUnattended(prompt string, defInteractive, defUnattended bool) bool {
	if pc.yes {
		return defUnattended
	}
	suffix := "[y/N]"
	if defInteractive {
		suffix = "[Y/n]"
	}
	ans := strings.TrimSpace(readSingleKey(prompt + " " + suffix + ": "))
	if ans == "" {
		return defInteractive
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
		{"local-models", "Local models (optional)", provPhaseLocalModels},
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
		if !pc.incomplete[ph.id] {
			st.markDone(ph.id)
		}
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
				pc.addManualFor(whyHost, "Disable sleep", "sudo pmset -a sleep 0 disablesleep 1 womp 1 autorestart 1")
			} else if after := pmsetValues(); after["sleep"] == "0" {
				fmt.Println("  ✓ power settings applied (verified: sleep is off)")
			} else {
				fmt.Println("  ⚠ pmset ran but sleep still isn't 0 — check `pmset -g`")
			}
		} else {
			pc.addManualFor(whyHost, "Disable sleep", "sudo pmset -a sleep 0 disablesleep 1 womp 1 autorestart 1")
		}
	}

	// Auto-login lets LaunchAgents come back after an unattended reboot. It can't
	// be flipped safely from the CLI (it stores the account password), so this is
	// always a GUI instruction.
	fmt.Println()
	fv := fileVaultOn()
	if fv {
		fmt.Println("  Auto-login: macOS won't allow it while FileVault is on, and a FileVault Mac")
		fmt.Println("  stops at the unlock screen at boot anyway — so this needs a decision, below.")
	} else {
		fmt.Println("  Auto-login (so the daemon returns after a reboot with no one at the keyboard):")
		fmt.Println("    System Settings → Users & Groups → Automatically log in as → <your user>")
		fmt.Println("    (Optional if you installed the daemon as a --system LaunchDaemon, which")
		fmt.Println("     starts at boot before any login.)")
	}
	pc.guide(autoLoginStep(fv))
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

// tailscaleUpHeadlessHint is the browser-free way onto the tailnet, for a
// headless Mac mini (the provisioning target). An auth key from the admin
// console replaces the interactive login `tailscale up` would otherwise need.
// tailscaleUpCmdHint is the `tailscale up` guidance, and it carries two caveats
// because BOTH bit the same operator on the same day.
//
// On macOS, `brew install tailscale` leaves `tailscale up` failing with "failed
// to connect to local Tailscale service; is Tailscale running?" — but NOT
// because the formula is CLI-only, which is what this said. The formula ships
// tailscaled; nothing starts it. `sudo brew services start tailscale` does, and
// an operator who ran it (Cellar/tailscale/*/bin/tailscaled, started cleanly) is
// what corrected this. Prescribing a reinstall for a service that is installed
// costs a download and still leaves them one command short, so name that command
// first and keep the cask as the alternative it is.
//
// And on a node that is ALREADY on a tailnet, `tailscale up` with a partial flag
// set refuses: "changing settings via 'tailscale up' requires mentioning all
// non-default flags". Prescribing an exact flag list is therefore wrong for every
// configured machine — so point at the command Tailscale itself prints rather
// than pretending we know their flags.
const tailscaleUpCmdHint = "sudo tailscale up --ssh --accept-dns=true\n" +
	"(\"is Tailscale running?\" — start the service first, then re-run the above:\n" +
	"   sudo brew services start tailscale        [Homebrew formula]\n" +
	"   or install the GUI app: brew install --cask tailscale-app, then open it)\n" +
	"(already on a tailnet? Tailscale will print the exact command to use — run that one)"

const tailscaleUpHeadlessHint = "Headless / no browser here? Generate an auth key " +
	"(Tailscale admin console → Settings → Keys → Generate auth key), then run: " +
	"sudo tailscale up --ssh --accept-dns=true --auth-key=tskey-auth-xxxxx"

// recordTailnetAddrs stashes this host's tailnet FQDN and IPv4 (once Tailscale is
// up) into the resumable provisioning state, so the verify/handoff phase can
// print a reachable DEJIMA_HOST — the IP especially, which a just-shared teammate
// can dial before their MagicDNS is set up.
func recordTailnetAddrs(pc *provCtx) {
	if fqdn := tailnetFQDN(); fqdn != "" {
		pc.state.Answers["tailnet_fqdn"] = fqdn
	}
	if ip, ok := tailscaleIPv4(); ok {
		pc.state.Answers["tailnet_ip"] = ip
	}
}

func provPhaseTooling(pc *provCtx) error {
	// This phase installs Docker Desktop and Tailscale into /Applications, which
	// macOS gates behind a permission prompt — and attributes to the TERMINAL
	// APP, not to Dejima. So the operator gets `"Ghostty" would like to access
	// data from other apps` in the middle of an install they started, naming a
	// program they did not think was involved. Reported as "super weird and
	// disconcerting", which is the correct reaction to an unexplained prompt
	// about one app reaching into others. Say it is coming, and why, first.
	fmt.Println("  Heads-up: macOS may ask whether your terminal app can \"access data from")
	fmt.Println("  other apps\" or manage other applications. That's this step installing")
	fmt.Println("  Docker and Tailscale into /Applications — macOS credits the prompt to the")
	fmt.Println("  terminal you're typing in, not to Dejima. Allow it, or those installs fail.")
	fmt.Println()

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
				pc.addManualFor(whyBlocking, "Install Homebrew", script)
			}
		} else {
			pc.addManualFor(whyBlocking, "Install Homebrew, then re-run this", "see https://brew.sh")
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
			stopSudo := primeSudo("Installing Tailscale")
			err := execInteractive("brew", "install", "--cask", "tailscale-app")
			stopSudo()
			if err != nil {
				fmt.Printf("  ✗ install failed: %v\n", err)
				pc.addManualFor(whyRemote, "Install Tailscale", "brew install --cask tailscale-app   (or https://tailscale.com/download)")
			}
		} else if !brewAvail {
			pc.addManualFor(whyRemote, "Install Tailscale (after Homebrew is in place)", "brew install --cask tailscale-app")
		}
	} else {
		fmt.Println("  ✓ Tailscale present")
	}
	// Bring the tailnet up with the SSH server on (idempotent; opens a browser the
	// first time). Off-box account login is inherently interactive; on a headless
	// Mac mini (the common case) there's no browser, so the auth-key route is the
	// real path — surface it whenever Tailscale isn't already up.
	if _, err := exec.LookPath("tailscale"); err == nil {
		if st := tailscaleStatus(); st.BackendState == "Running" {
			fmt.Println("  ✓ Tailscale is up")
			recordTailnetAddrs(pc)
		} else if pc.yes {
			// Non-interactive run: can't do a browser login. The auth-key route is
			// the headless path, so lead with it.
			pc.addManualFor(whyRemote, "Bring Tailscale up (headless — no browser)", tailscaleUpHeadlessHint)
			pc.addManualFor(whyRemote, "…or interactively, at this Mac's screen", tailscaleUpCmdHint)
		} else {
			fmt.Println("  Tailscale isn't up yet (needed for remote + team access).")
			fmt.Println("  " + tailscaleUpHeadlessHint)
			if pc.confirm("  Bring Tailscale up now (opens a browser to log in)?", true) {
				if err := execInteractive("tailscale", "up", "--ssh", "--accept-dns=true"); err != nil {
					fmt.Printf("  ✗ `tailscale up` failed: %v\n", err)
					pc.addManualFor(whyRemote, "Bring Tailscale up", tailscaleUpCmdHint)
				} else {
					recordTailnetAddrs(pc)
					if fqdn := pc.state.Answers["tailnet_fqdn"]; fqdn != "" {
						fmt.Printf("  ✓ on the tailnet as %s\n", fqdn)
					}
				}
			} else {
				pc.addManualFor(whyRemote, "Bring Tailscale up", tailscaleUpCmdHint)
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
				stopSudo := primeSudo("Installing Docker Desktop")
				err := execInteractive("brew", "install", "--cask", "docker-desktop")
				stopSudo()
				if err != nil {
					fmt.Printf("  ✗ install failed: %v\n", err)
					pc.addManualFor(whyBlocking, "Install Docker Desktop", "brew install --cask docker-desktop")
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
				pc.addManualFor(whyBlocking, "Start Docker Desktop, then confirm it is up", "docker version   (must print a Server section)")
			}
		} else if !dockerReachable() {
			pc.addManualFor(whyBlocking, "Start Docker Desktop, then confirm it is up", "docker version   (must print a Server section)")
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
				pc.addManual("Authenticate gh (only needed for private repos)", "gh auth login")
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
	recGB := vmmem.RecommendedGB(host)
	fmt.Printf("  ⚠ Docker VM has only %s of %s host RAM — islands share this pool and will OOM.\n",
		humanBytes(vm), humanBytes(host))
	fmt.Printf("    Set it to %dGB.\n", recGB)

	// colima can resize from the CLI, so do it inline — suggest the vmmem default
	// (¾ of host RAM, leaving the host ≥4GiB) but let the user confirm or override
	// the number. Docker Desktop has no CLI resize (its memory is a GUI slider), so
	// that path keeps the doctor/checklist hint.
	if vmmem.ColimaAvailable() {
		fmt.Printf("    Recommended: ~%dGB (~¾ of RAM, leaving ≥4GB for the host). colima can apply this directly.\n", recGB)
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
				pc.addManualFor(whyHost, fmt.Sprintf("Set the Docker VM memory to %dGB (when islands are idle)", gb), fmt.Sprintf("colima start --memory %d", gb))
				return nil
			}
		}

		// Omit --cpu/--disk so colima keeps its saved values for those.
		if err := execInteractive("colima", "start", "--memory", strconv.Itoa(gb)); err != nil {
			fmt.Printf("  ✗ colima start --memory %d: %v\n", gb, err)
			title, detail := vmRightsizeStep(gb)
			pc.addManualFor(whyHost, title, detail)
			return nil
		}
		fmt.Printf("  ✓ ran: colima start --memory %d\n", gb)
		return nil
	}

	// Docker Desktop — no CLI resize; point at doctor --fix / the GUI slider.
	fmt.Printf("    Set Memory to %dGB: Docker Desktop → Settings → Resources → Memory.\n", recGB)
	fmt.Println("    (`dejima doctor --fix` scripts the equivalent colima resize.)")
	if pc.confirm("  Run `dejima doctor --fix` now?", true) {
		if self, err := os.Executable(); err == nil {
			if err := execInteractive(self, "doctor", "--fix"); err != nil {
				fmt.Printf("  ✗ doctor --fix: %v\n", err)
				title, detail := vmRightsizeStep(recGB)
				pc.guide(guidedStep{
					why:    whyHost,
					title:  title,
					detail: detail,
					verify: func() bool { return !vmmem.Undersized(vmmem.HostMemoryBytes(), dockerVMMemoryBytes()) },
					done:   "the Docker VM has enough memory for islands",
					notYet: fmt.Sprintf("still reads as under %dGB — Docker Desktop restarts its VM when you apply", recGB),
				})
			}
		}
	} else {
		title, detail := vmRightsizeStep(recGB)
		pc.addManualFor(whyHost, title, detail)
	}
	return nil
}

// vmRightsizeStep phrases the right-sizing step so the TARGET SIZE is in it.
// "Right-size the Docker VM" with the click path but no number is a step the
// operator cannot act on — they are left to work out the figure the wizard
// already computed. The field note was that it should blatantly say what to set
// it to, so every path that records this step goes through here.
func vmRightsizeStep(gb int) (title, detail string) {
	return fmt.Sprintf("Set the Docker VM memory to %dGB", gb),
		fmt.Sprintf("Docker Desktop → Settings → Resources → Memory → %dGB\n(or, with colima: colima start --memory %d)", gb, gb)
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
				pc.guide(guidedStep{
					why:    whyRemote,
					title:  "Enable Remote Login (SSH)",
					detail: "System Settings → General → Sharing → Remote Login\n(or: sudo systemsetup -setremotelogin on)",
					verify: remoteLoginOn,
					done:   "Remote Login is on",
					notYet: "still reads as off",
				})
			} else {
				fmt.Println("  ✓ Remote Login enabled")
			}
		} else {
			pc.guide(guidedStep{
				why:    whyRemote,
				title:  "Enable Remote Login (SSH)",
				detail: "System Settings → General → Sharing → Remote Login\n(or: sudo systemsetup -setremotelogin on)",
				verify: remoteLoginOn,
				done:   "Remote Login is on",
				notYet: "still reads as off",
			})
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
			pc.addManualFor(whyBlocking, "Install the Dejima server", "git clone …/dejima && make setup")
			return nil
		}
	}

	// Install (or reinstall) dejimad as a boot LaunchDaemon with the recommended
	// host posture: tailnet TCP for remote clients, the in-island autonomy path,
	// and the operational audit log on.
	//
	// The tailnet listener needs Tailscale actually signed in — and by this point
	// we've already learned whether it is. Baking --tcp in regardless is how a
	// fresh mini ended up with a daemon that couldn't start: the wizard puts
	// "bring Tailscale up" on the manual checklist and then installs a service
	// whose precondition that checklist item IS. The daemon now degrades instead
	// of dying, so this isn't load-bearing anymore, but promising remote access
	// we can't deliver yet is still the wrong thing to print.
	tailnetUp := tailscaleStatus().BackendState == "Running"
	fmt.Println("  Installing dejimad as a system service (starts at boot, no login needed) with:")
	if tailnetUp {
		fmt.Println("    • remote access on :7273 (tailnet peers only)")
	} else {
		fmt.Println("    • remote access on :7273 — Tailscale isn't up yet, so this comes online")
		fmt.Println("      by itself once you finish `sudo tailscale up --ssh --accept-dns=true`")
	}
	fmt.Println("    • in-island autonomy on 127.0.0.1:7274")
	fmt.Println("    • the operational audit log (--audit)")
	const svcHint = "Install the daemon: dejima service install --system --tcp :7273 --token-tcp 127.0.0.1:7274 --audit"
	if pc.confirm("  Install the service now (sudo)?", true) {
		args := []string{"service", "install", "--system",
			"--tcp", ":7273", "--token-tcp", "127.0.0.1:7274", "--audit",
			"--no-tcp-prompt", "--no-notify-prompt"}
		if err := execInteractive(self, args...); err != nil {
			fmt.Printf("  ✗ service install: %v\n", err)
			pc.addManualFor(whyHost, "Register dejimad as a service", svcHint)
			return nil
		}
		fmt.Println("  ✓ daemon installed and supervised")
		if !tailnetUp {
			pc.addManualFor(whyRemote,
				"Remote access on :7273 is waiting on Tailscale (the daemon picks it up within a minute — no restart)",
				tailscaleUpCmdHint)
		}
	} else {
		pc.addManualFor(whyHost, "Register dejimad as a service", svcHint)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Phase 6 — local models (optional): the cloud/local choice
// ---------------------------------------------------------------------------

// provPhaseLocalModels offers the "run open-weights models on this host" path.
// Opt-in (a model is a multi-GB download), so it defaults to no — and under
// --yes it's skipped entirely rather than pulling gigabytes unattended. When
// taken it installs the backend + pulls the host-recommended model via the
// freshly-installed daemon; anything not auto-done becomes a manual hint.
func provPhaseLocalModels(pc *provCtx) error {
	fmt.Println("  Optional: run open-weights models (Qwen-Coder, Mistral, …) on THIS host so")
	fmt.Println("  your isolated agents can use them — no per-token cloud cost, nothing leaves")
	fmt.Println("  the machine. The model loads once here; islands share it as an OpenAI-")
	fmt.Println("  compatible endpoint. (You can always do this later with `dejima local`.)")
	if !pc.confirmUnattended("  Set up local models now (installs Ollama + a recommended model)?", true, false) {
		fmt.Println("  Skipped. Set them up any time from the TUI: run `dejima`, Local models.")
		return nil
	}
	c, err := client()
	if err != nil {
		fmt.Println("  The daemon wasn't up yet. Set them up from the TUI later: run `dejima`, Local models.")
		pc.markIncomplete("local-models")
		return nil
	}
	fmt.Println("  Installing the inference backend (Ollama)…")
	// Install from here, not through the daemon: the wizard runs in the
	// operator's terminal, and on macOS the backend's installer needs one for
	// sudo. See installLocalBackendHere.
	installLocalBackendHere(pc.ctx)
	if err := c.LocalInstall(pc.ctx, os.Stdout); err != nil {
		fmt.Printf("  Install didn't finish (%v). Retry any time from the TUI: run `dejima`, Local models.\n", err)
		pc.markIncomplete("local-models")
		return nil
	}
	st, err := c.LocalStatus(pc.ctx)
	if err != nil || st.Recommend.Top == nil {
		fmt.Println("  Pick a model from the TUI when you want one: run `dejima`, Local models.")
		pc.markIncomplete("local-models")
		return nil
	}
	top := st.Recommend.Top
	if !pc.confirm(fmt.Sprintf("  Pull the recommended model for this host — %s (%s)?", top.Alias, top.Params), true) {
		fmt.Println("  Pull one from the TUI when you want it: run `dejima`, Local models.")
		return nil
	}
	fmt.Printf("  Pulling %s…\n", top.Alias)
	if err := c.PullLocalModel(pc.ctx, top.Alias, os.Stdout); err != nil {
		fmt.Printf("  The %s pull didn't finish. Retry from the TUI: run `dejima`, Local models.\n", top.Alias)
		pc.markIncomplete("local-models")
		return nil
	}
	fmt.Println("  ✓ local models ready — point an agent at the `local` provider (the `v` model editor).")
	return nil
}

// ---------------------------------------------------------------------------
// Phase 7 — verify + handoff
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
	ip := pc.state.Answers["tailnet_ip"]
	if ip == "" {
		if v, ok := tailscaleIPv4(); ok {
			ip = v
		}
	}
	user := currentUnixUser()

	// Two audiences, two right answers, and the old code served only the second.
	//
	// YOUR OWN devices are on this tailnet with MagicDNS, so the NAME is the
	// better address: it survives the IP changing, which it will. A TEAMMATE
	// whose node was only just shared may not resolve MagicDNS yet, so they want
	// the IP. Leading with the IP for everyone meant the operator copied an
	// address that quietly rots, and the name appeared as a footnote nobody read.
	//
	// So: lead with the name when there is one, and give the IP as the labelled
	// fallback rather than the headline.
	remote := ip
	if fqdn != "" {
		remote = fqdn
	}

	fmt.Println()
	fmt.Println(bold("  This host is ready. From your other devices:"))
	fmt.Println()
	if remote != "" {
		// VERIFY, don't just claim: does the daemon's tailnet TCP listener actually
		// answer? Closes the loop so the host comes up demonstrably reachable.
		if tcpReachable(net.JoinHostPort(remote, "7273")) {
			fmt.Printf("    ✓ Reachable on the tailnet at %s:7273\n", remote)
		} else {
			fmt.Printf("    ⚠ %s:7273 isn't answering yet — confirm the service is running\n", remote)
			fmt.Println("      (`dejima service install --system --tcp :7273 …`) and Tailscale is up.")
		}
		fmt.Println()
		// WRITE THE ADDRESS DOWN NOW. The client installer asks for it on the
		// OTHER machine, at which point the operator is standing at a laptop
		// being asked for a number that is only printed here — so they go
		// hunting, or guess. Say it while they are still at this Mac, and say
		// what will ask for it. (`go install` used to lead here: the developer
		// path, for a person who just wants their laptop to talk to this Mac.)
		fmt.Println("    On your laptop (signed in to the same Tailscale account), run:")
		fmt.Println("      curl -fsSL https://dejima.tech/install-client.sh | bash")
		fmt.Println()
		fmt.Println("    It asks for a \"Server address\". That is this Mac. Type:")
		fmt.Printf("      %s\n", remote)
		fmt.Println()
		fmt.Println("    (If your laptop is already on this tailnet, the installer finds this Mac")
		fmt.Println("     by itself and offers that address — just press Enter.)")
		if fqdn != "" && ip != "" {
			fmt.Println()
			fmt.Printf("    That name (%s) is this Mac's MagicDNS name — Tailscale's own DNS.\n", fqdn)
			fmt.Println("    Prefer it over the IP: it keeps working when the address changes.")
			fmt.Printf("    If a device can't resolve it yet, use the IP instead: %s:7273\n", ip)
		} else if fqdn == "" && ip != "" {
			fmt.Println()
			fmt.Println("    No MagicDNS name yet — that's Tailscale admin console → DNS → enable")
			fmt.Println("    MagicDNS. Worth doing: the name survives the IP changing, and it will.")
		}
		if user != "" && fqdn != "" {
			fmt.Printf("    Or open a shell on the host:  ssh %s@%s\n", user, fqdn)
		}
		fmt.Println()
		fmt.Println("    To add a teammate: run `dejima`, press [I] to mint an invite, and share THIS")
		fmt.Println("    node to their Tailscale (admin console → Machines → Share) — just this machine.")
	} else {
		fmt.Println("    (Bring Tailscale up — `tailscale up --ssh`, or the auth-key route for a")
		fmt.Println("     headless host — then: export DEJIMA_HOST=<host>:7273 && dejima ls)")
	}
	maybeWriteCheatsheet(pc, fqdn, user)
	return nil
}

// printProvManual prints the accumulated GUI-only / off-box checklist.
func printProvManual(pc *provCtx) { fmt.Print(renderProvManual(pc)) }

// renderProvManual returns the checklist as text. Split from printing so the
// layout can be asserted in a test: this is the last thing a new operator reads,
// and it is the surface a field report was filed against.
func renderProvManual(pc *provCtx) string {
	if len(pc.manual) == 0 {
		return ""
	}
	var b strings.Builder
	// Grouped by consequence, blocking first, optional last — and each step is a
	// TITLE on its own line with the command indented beneath it. The flat
	// version put a dozen equal-weight bullets in front of a new operator with
	// the command buried mid-sentence, and the field report was "That's a lot of
	// steps". Most of them were optional; nothing said so.
	order := []string{whyBlocking, whyRemote, whyHost, ""}
	groups := map[string][]manualStep{}
	for _, m := range pc.manual {
		groups[m.why] = append(groups[m.why], m)
	}

	b.WriteString("\n")
	b.WriteString(bold("Still to do by hand") + "\n")
	// Say up front whether the install is finished, because that is the question
	// the list actually raises and the flat version never answered it.
	if len(groups[whyBlocking]) == 0 {
		b.WriteString("Dejima works now — nothing below blocks using it on this Mac.\n")
	} else {
		b.WriteString("Some of these are required. They are listed first.\n")
	}

	for _, why := range order {
		steps := groups[why]
		if len(steps) == 0 {
			continue
		}
		header := why
		if header == "" {
			header = "Optional"
		}
		b.WriteString("\n")
		b.WriteString("  " + header + "\n")
		for _, m := range steps {
			fmt.Fprintf(&b, "  • %s\n", m.title)
			for _, line := range strings.Split(m.detail, "\n") {
				if strings.TrimSpace(line) != "" {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
		}
	}
	b.WriteString("\n")
	return b.String()
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
