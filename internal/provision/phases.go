package provision

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// These phases gate on Context.OS at runtime (not build tags) so the package
// compiles on every target the CLI cross-compiles for; only the matching OS's
// phases are ever in a returned list (see PhasesFor).

// --- macOS system config (GUI-instructed) ---------------------------------

type systemConfigPhase struct{}

func (systemConfigPhase) ID() string    { return "system-config" }
func (systemConfigPhase) Title() string { return "System config (sleep, remote login)" }

func (p systemConfigPhase) Detect(c *Context) (Status, string, error) {
	// All sub-settings automatable via pmset/systemsetup, but several need sudo
	// and a couple are GUI-only. Treat the phase as needing action unless the
	// key reboot-survival settings already read correctly.
	if c.OS != "darwin" {
		return StatusSkip, "not macOS", nil
	}
	sleep, _ := c.Runner.Run("pmset", "-g")
	remote, _ := c.Runner.Run("systemsetup", "-getremotelogin")
	if strings.Contains(sleep, " sleep                0") && strings.Contains(strings.ToLower(remote), "on") {
		return StatusDone, "sleep disabled and Remote Login on", nil
	}
	return StatusNeedsAct, "this Mac should never sleep and must allow Remote Login (SSH)", nil
}

func (p systemConfigPhase) Act(c *Context) error {
	// Automatable with sudo: prevent sleep, wake on network, auto-restart, SSH.
	c.say("  applying power + remote-login settings (sudo may prompt):")
	steps := [][]string{
		{"sudo", "pmset", "-a", "sleep", "0"},
		{"sudo", "pmset", "-a", "womp", "1"},
		{"sudo", "pmset", "-a", "autorestart", "1"},
		{"sudo", "systemsetup", "-setremotelogin", "on"},
	}
	for _, s := range steps {
		c.say("    $ %s", strings.Join(s, " "))
		if out, err := c.Runner.Run(s[0], s[1:]...); err != nil {
			// pmset/systemsetup vary by hardware (e.g. no battery → some keys
			// unsupported); report but don't abort the whole phase.
			c.say("      note: %v %s", err, out)
		}
	}
	return nil
}

func (p systemConfigPhase) Verify(c *Context) (bool, string, error) {
	remote, _ := c.Runner.Run("systemsetup", "-getremotelogin")
	if !strings.Contains(strings.ToLower(remote), "on") {
		return false, "Remote Login still off — enable it in System Settings → General → Sharing → Remote Login", nil
	}
	return true, "power settings applied, Remote Login on", nil
}

// --- tooling (brew on macOS, apt/dnf on Linux) -----------------------------

type toolingPhase struct{}

func (toolingPhase) ID() string    { return "tooling" }
func (toolingPhase) Title() string { return "Tooling (Docker, Tailscale, gh)" }

func (p toolingPhase) Detect(c *Context) (Status, string, error) {
	missing := p.missing(c)
	if len(missing) == 0 {
		return StatusDone, "docker, tailscale, gh all present", nil
	}
	return StatusNeedsAct, "missing: " + strings.Join(missing, ", "), nil
}

func (p toolingPhase) missing(c *Context) []string {
	var missing []string
	for _, t := range []string{"docker", "tailscale", "gh"} {
		if !c.Runner.Look(t) {
			missing = append(missing, t)
		}
	}
	return missing
}

func (p toolingPhase) Act(c *Context) error {
	missing := p.missing(c)
	if c.OS == "darwin" {
		if !c.Runner.Look("brew") {
			c.say("  Homebrew is required to install tooling on macOS.")
			c.say("  install it: /bin/bash -c \"$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\"")
			c.pauseForGUI("  install Homebrew, then continue.")
		}
		for _, t := range missing {
			pkg, cask := brewSpec(t)
			args := []string{"install"}
			if cask {
				args = append(args, "--cask")
			}
			args = append(args, pkg)
			c.say("    $ brew %s", strings.Join(args, " "))
			if out, err := c.Runner.Run("brew", args...); err != nil {
				c.say("      %v %s", err, out)
			}
			if t == "docker" || t == "tailscale" {
				c.pauseForGUI(fmt.Sprintf("  launch %s once and grant permissions / sign in, then continue.", t))
			}
		}
		return nil
	}
	// Linux
	mgr := detectLinuxPkgMgr(c.Runner)
	if mgr == "" {
		return fmt.Errorf("no supported package manager (apt/dnf) found")
	}
	for _, t := range missing {
		c.say("  installing %s via %s (sudo may prompt):", t, mgr)
		for _, cmd := range linuxInstallCmds(mgr, t) {
			c.say("    $ %s", strings.Join(cmd, " "))
			if out, err := c.Runner.Run(cmd[0], cmd[1:]...); err != nil {
				c.say("      %v %s", err, out)
			}
		}
		if t == "tailscale" {
			c.pauseForGUI("  bring Tailscale up: `sudo tailscale up --ssh`, then continue.")
		}
	}
	return nil
}

func (p toolingPhase) Verify(c *Context) (bool, string, error) {
	if m := p.missing(c); len(m) > 0 {
		return false, "still missing: " + strings.Join(m, ", "), nil
	}
	return true, "docker, tailscale, gh installed", nil
}

// brewSpec maps a tool to its Homebrew package and whether it's a cask.
func brewSpec(tool string) (pkg string, cask bool) {
	switch tool {
	case "docker":
		return "docker", true
	case "tailscale":
		return "tailscale", true
	default: // gh
		return "gh", false
	}
}

// linuxInstallCmds returns the package-manager commands to install a tool.
func linuxInstallCmds(mgr, tool string) [][]string {
	if tool == "tailscale" {
		// Tailscale ships its own installer that configures the apt/dnf repo.
		return [][]string{{"sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh"}}
	}
	pkg := tool // docker → docker.io on apt; gh → gh
	switch mgr {
	case "apt":
		if tool == "docker" {
			pkg = "docker.io"
		}
		return [][]string{
			{"sudo", "apt-get", "update", "-y"},
			{"sudo", "apt-get", "install", "-y", pkg},
		}
	case "dnf":
		if tool == "docker" {
			pkg = "docker"
		}
		return [][]string{{"sudo", "dnf", "install", "-y", pkg}}
	}
	return nil
}

// --- shell / ssh config ----------------------------------------------------

type shellSSHPhase struct{}

func (shellSSHPhase) ID() string    { return "shell-ssh" }
func (shellSSHPhase) Title() string { return "Shell & SSH config" }

func (p shellSSHPhase) Detect(c *Context) (Status, string, error) {
	// Always run: the PATH line and authorized_keys are cheap to re-assert and
	// AppendLineIfAbsent is idempotent.
	return StatusNeedsAct, "ensure non-interactive SSH inherits PATH; offer key-only auth", nil
}

func (p shellSSHPhase) Act(c *Context) error {
	u, err := realUser()
	if err != nil {
		return err
	}
	if c.OS == "darwin" {
		// Non-interactive SSH on macOS doesn't get Homebrew's bin on PATH; add it
		// so `dejima`/`docker` resolve over Remote-SSH.
		zshenv := filepath.Join(u.HomeDir, ".zshenv")
		if err := AppendLineIfAbsent(zshenv, `export PATH="/opt/homebrew/bin:$PATH"`); err != nil {
			c.say("  note: couldn't update %s: %v", zshenv, err)
		} else {
			c.say("  ensured /opt/homebrew/bin on PATH in ~/.zshenv")
		}
	}
	// Opt-in, always-explicit SSH password disable (never under --yes alone).
	if c.OS != "windows" {
		if c.DisableSSHPassword && c.confirmExplicit("  disable SSH password auth (key-only)? this can lock you out if no key works") {
			cmds := [][]string{
				{"sudo", "sed", "-i.bak", "s/^#*PasswordAuthentication yes/PasswordAuthentication no/", sshdConfigPath(c.OS)},
			}
			for _, cmd := range cmds {
				if out, err := c.Runner.Run(cmd[0], cmd[1:]...); err != nil {
					c.say("  note: %v %s", err, out)
				}
			}
			c.setAnswer("ssh_password_disabled", true)
			c.say("  SSH password auth disabled (key-only).")
		} else {
			c.setAnswer("ssh_password_disabled", false)
			c.say("  left SSH password auth as-is (pass --disable-ssh-password to harden).")
		}
	}
	return nil
}

func (p shellSSHPhase) Verify(c *Context) (bool, string, error) {
	return true, "shell/ssh config applied", nil
}

func sshdConfigPath(goos string) string { return "/etc/ssh/sshd_config" }

// --- Linux systemd lingering ----------------------------------------------

type lingerPhase struct{}

func (lingerPhase) ID() string    { return "linger" }
func (lingerPhase) Title() string { return "Persistent user services (linger)" }

func (p lingerPhase) Detect(c *Context) (Status, string, error) {
	if c.OS != "linux" {
		return StatusSkip, "not Linux", nil
	}
	u, err := realUser()
	if err != nil {
		return StatusNeedsAct, "", nil
	}
	out, _ := c.Runner.Run("loginctl", "show-user", u.Username, "--property=Linger")
	if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(out, "Linger=")), "yes") {
		return StatusDone, "linger already enabled — user services survive logout/reboot", nil
	}
	return StatusNeedsAct, "without linger, dejimad's user service dies on logout", nil
}

func (p lingerPhase) Act(c *Context) error {
	u, err := realUser()
	if err != nil {
		return err
	}
	c.say("  $ loginctl enable-linger %s", u.Username)
	if out, err := c.Runner.Run("loginctl", "enable-linger", u.Username); err != nil {
		return fmt.Errorf("enable-linger: %w: %s", err, out)
	}
	return nil
}

func (p lingerPhase) Verify(c *Context) (bool, string, error) {
	u, err := realUser()
	if err != nil {
		return false, err.Error(), nil
	}
	out, _ := c.Runner.Run("loginctl", "show-user", u.Username, "--property=Linger")
	if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(out, "Linger=")), "yes") {
		return true, "linger enabled", nil
	}
	return false, "linger still off", nil
}

// --- Dejima install (handoff to existing logic) ----------------------------

type installPhase struct{}

func (installPhase) ID() string    { return "dejima-install" }
func (installPhase) Title() string { return "Install Dejima (image + daemon service)" }

func (p installPhase) Detect(c *Context) (Status, string, error) {
	// Reachable daemon means it's installed; supervision/reboot-durability is
	// checked by `dejima doctor` in the verify phase, so here we only gate on
	// "is the daemon up?".
	if out, err := c.Runner.Run(c.self(), "doctor"); err == nil && strings.Contains(out, "daemon") {
		// doctor exits non-zero on any FAIL, so err==nil means healthy.
		return StatusDone, "daemon already installed and healthy", nil
	}
	return StatusNeedsAct, "build the island image and install the daemon as a service", nil
}

func (p installPhase) Act(c *Context) error {
	// Build the island image (reuses `dejima image build`).
	c.say("  $ %s image build", c.self())
	if out, err := c.Runner.Run(c.self(), "image", "build"); err != nil {
		c.say("    note: image build: %v %s", err, out)
	}
	// Install the daemon as a service. On a headless Mac, the boot-durable
	// system LaunchDaemon is required; hand --system to the existing command.
	args := []string{"service", "install"}
	if c.OS == "darwin" && isHeadlessMac(c.Runner) {
		args = append(args, "--system")
		c.say("  headless Mac detected → installing boot-durable system LaunchDaemon")
	}
	c.say("  $ %s %s", c.self(), strings.Join(args, " "))
	if out, err := c.Runner.Run(c.self(), args...); err != nil {
		return fmt.Errorf("service install: %w: %s", err, out)
	}
	return nil
}

func (p installPhase) Verify(c *Context) (bool, string, error) {
	if _, err := c.Runner.Run(c.self(), "doctor"); err != nil {
		return false, "daemon not healthy yet — see `dejima doctor`", nil
	}
	return true, "daemon installed and reachable", nil
}

// --- final verify + handoff ------------------------------------------------

type verifyPhase struct{}

func (verifyPhase) ID() string    { return "verify" }
func (verifyPhase) Title() string { return "Verify & connection info" }

func (p verifyPhase) Detect(c *Context) (Status, string, error) {
	return StatusNeedsAct, "run a final health check and print client connection info", nil
}

func (p verifyPhase) Act(c *Context) error {
	out, err := c.Runner.Run(c.self(), "doctor")
	c.say("%s", out)
	if err != nil {
		c.say("  some checks still fail — `dejima doctor --fix` may resolve them.")
	}
	// Connection info for client devices.
	fqdn, _ := c.Runner.Run("tailscale", "status", "--json")
	c.say("\n✅ Setup complete.")
	if strings.Contains(fqdn, "DNSName") {
		c.say("  This host is reachable over Tailscale. From a client device:")
	}
	c.say("    go install github.com/aoos/dejima/cmd/dejima@latest")
	c.say("    export DEJIMA_HOST=<this-host>.<tailnet>.ts.net:7273")
	c.say("    dejima ls")
	return nil
}

func (p verifyPhase) Verify(c *Context) (bool, string, error) {
	return true, "done", nil
}

// self resolves the dejima binary path for handoff steps.
func (c *Context) self() string {
	if c.SelfBin != "" {
		return c.SelfBin
	}
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "dejima"
}
