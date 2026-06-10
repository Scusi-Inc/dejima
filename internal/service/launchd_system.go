package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"text/template"
)

const launchdSystemPlist = "/Library/LaunchDaemons/" + launchdLabel + ".plist"

// launchdSystemManager installs dejimad as a system-wide LaunchDaemon,
// bootstrapped into launchd's `system` domain. Unlike a per-user LaunchAgent
// (gui domain), the system domain exists from boot with no console login —
// this is the mode for headless Macs. The daemon still runs as the installing
// user via the plist's UserName key; the privileged steps (writing to
// /Library/LaunchDaemons, system-domain launchctl) go through sudo.
//
// Caveat: at boot, before any login, the user's login keychain is locked, so
// keychain-backed agent credentials may be unavailable until the user logs in
// (SSH or desktop) at least once per boot.
type launchdSystemManager struct{}

func (m *launchdSystemManager) Install(binaryPath string, args []string) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	logDir := filepath.Join(u.HomeDir, "Library", "Logs", "dejima")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}

	tmpl := template.Must(template.New("plist").Parse(launchdTemplate))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"Label":            launchdLabel,
		"ProgramArguments": append([]string{binaryPath}, args...),
		"WorkingDir":       u.HomeDir,
		"UserName":         u.Username,
		"StdoutPath":       filepath.Join(logDir, "dejimad.out.log"),
		"StderrPath":       filepath.Join(logDir, "dejimad.err.log"),
	}); err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", launchdLabel+"-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// A leftover per-user LaunchAgent would race the system daemon for the
	// socket and TCP port (now or at the next desktop login) — retire it.
	agent := &launchdManager{}
	if p, perr := agent.plistPath(); perr == nil {
		if _, statErr := os.Stat(p); statErr == nil {
			_ = agent.Uninstall()
			fmt.Println("removed per-user LaunchAgent (superseded by the system daemon)")
		}
	}

	fmt.Println("installing system LaunchDaemon (sudo may prompt for your password)…")
	// launchd refuses plists in /Library/LaunchDaemons unless they are
	// root-owned and not group/world-writable; `install` sets all of that in
	// one step.
	if stderr, err := runCaptureStderr("sudo", "install", "-m", "0644", "-o", "root", "-g", "wheel",
		tmp.Name(), launchdSystemPlist); err != nil {
		return fmt.Errorf("sudo install %s: %w: %s", launchdSystemPlist, err, strings.TrimSpace(stderr))
	}
	_ = exec.Command("sudo", "launchctl", "bootout", "system/"+launchdLabel).Run()
	if stderr, err := runCaptureStderr("sudo", "launchctl", "bootstrap", "system", launchdSystemPlist); err != nil {
		return fmt.Errorf("launchctl bootstrap system: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

func (m *launchdSystemManager) Uninstall() error {
	_ = exec.Command("sudo", "launchctl", "bootout", "system/"+launchdLabel).Run()
	if stderr, err := runCaptureStderr("sudo", "rm", "-f", launchdSystemPlist); err != nil {
		return fmt.Errorf("sudo rm %s: %w: %s", launchdSystemPlist, err, strings.TrimSpace(stderr))
	}
	return nil
}

func (m *launchdSystemManager) Status() (string, error) {
	out, err := exec.Command("launchctl", "print", "system/"+launchdLabel).Output()
	if err != nil {
		return "not loaded (system domain)", nil
	}
	// `launchctl print` is verbose; surface just the interesting lines.
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "state = ") || strings.HasPrefix(t, "pid = ") ||
			strings.HasPrefix(t, "last exit code = ") {
			lines = append(lines, t)
		}
	}
	if len(lines) == 0 {
		return "loaded (system domain)", nil
	}
	return "loaded (system domain): " + strings.Join(lines, ", "), nil
}

func (m *launchdSystemManager) Restart() error {
	target := "system/" + launchdLabel
	if stderr, err := runCaptureStderr("sudo", "launchctl", "kickstart", "-k", target); err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w: %s — is the service installed? (`dejima service install --system`)",
			target, err, strings.TrimSpace(stderr))
	}
	return nil
}
