package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const launchdLabel = "dev.dejima.dejimad"

// launchdManager installs dejimad as a per-user LaunchAgent.
type launchdManager struct{}

func (m *launchdManager) plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func (m *launchdManager) Install(binaryPath string, args []string) error {
	path, err := m.plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, "Library", "Logs", "dejima")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}

	outLog := filepath.Join(logDir, "dejimad.out.log")
	errLog := filepath.Join(logDir, "dejimad.err.log")

	tmpl := template.Must(template.New("plist").Parse(launchdTemplate))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"Label":            launchdLabel,
		"ProgramArguments": append([]string{binaryPath}, args...),
		"WorkingDir":       home,
		"Home":             home,
		"StdoutPath":       outLog,
		"StderrPath":       errLog,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}

	// Idempotent: clear any prior load before re-loading. These are expected
	// to no-op if nothing's loaded; we ignore their errors.
	_ = exec.Command("launchctl", "bootout", "gui/"+currentUID()+"/"+launchdLabel).Run()
	_ = exec.Command("launchctl", "bootout", "user/"+currentUID()+"/"+launchdLabel).Run()
	_ = exec.Command("launchctl", "unload", path).Run()

	// Try the GUI domain first — that's where LaunchAgents normally live and
	// where launchd auto-loads them on desktop login. On a headless Mac (SSH,
	// no console login) the gui domain doesn't exist, but the per-user
	// `user/<uid>` domain does, so fall back to it: the daemon is then still
	// supervised (KeepAlive restarts) for this boot. The user domain is torn
	// down at shutdown and nothing re-bootstraps the agent at the next boot
	// until someone logs in, so it's not reboot-durable — for that, point at
	// the system LaunchDaemon install. `load -w` remains as a last resort for
	// old launchctl versions without `bootstrap`.
	stderrGUI, errGUI := runCaptureStderr("launchctl", "bootstrap", "gui/"+currentUID(), path)
	if errGUI == nil {
		return nil
	}
	if _, errUser := runCaptureStderr("launchctl", "bootstrap", "user/"+currentUID(), path); errUser == nil {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "note: no GUI session on this Mac — loaded dejimad into your user launchd")
		fmt.Fprintln(os.Stderr, "domain instead. It's supervised (auto-restarts on crash) but will NOT start")
		fmt.Fprintln(os.Stderr, "by itself after a reboot until someone logs in. For a headless Mac that")
		fmt.Fprintln(os.Stderr, "must survive reboots, install a system daemon instead (needs sudo):")
		fmt.Fprintf(os.Stderr, "  dejima service install --system %s\n", strings.Join(args, " "))
		fmt.Fprintln(os.Stderr)
		return nil
	}
	if stderr2, err2 := runCaptureStderr("launchctl", "load", "-w", path); err2 != nil {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "warning: plist written to %s, but launchctl couldn't load it.\n", path)
		fmt.Fprintf(os.Stderr, "  bootstrap → %v: %s\n", errGUI, strings.TrimSpace(stderrGUI))
		fmt.Fprintf(os.Stderr, "  load -w   → %v: %s\n", err2, strings.TrimSpace(stderr2))
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Ways forward:")
		fmt.Fprintf(os.Stderr, "  • Install as a system LaunchDaemon (loads at boot, no login needed):\n")
		fmt.Fprintf(os.Stderr, "      dejima service install --system %s\n", strings.Join(args, " "))
		fmt.Fprintln(os.Stderr, "  • Run dejimad manually for now (won't persist across reboots):")
		fmt.Fprintf(os.Stderr, "      nohup %s \\\n", strings.Join(append([]string{binaryPath}, args...), " "))
		fmt.Fprintf(os.Stderr, "        > %s \\\n", outLog)
		fmt.Fprintf(os.Stderr, "        2> %s < /dev/null &\n", errLog)
		fmt.Fprintln(os.Stderr, "      disown")
		fmt.Fprintln(os.Stderr)
		return nil // soft failure — plist is in place, user just needs to start dejimad
	}
	return nil
}

// runCaptureStderr is a small exec.Command wrapper that returns the command's
// stderr alongside its error, so the caller can surface launchctl's actual
// diagnostic instead of just an opaque exit code.
func runCaptureStderr(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

func (m *launchdManager) Uninstall() error {
	path, err := m.plistPath()
	if err != nil {
		return err
	}
	// Best-effort: bootout then unload, then remove the plist.
	_ = exec.Command("launchctl", "bootout", "gui/"+currentUID()+"/"+launchdLabel).Run()
	_ = exec.Command("launchctl", "bootout", "user/"+currentUID()+"/"+launchdLabel).Run()
	_ = exec.Command("launchctl", "unload", path).Run()
	return os.Remove(path)
}

func (m *launchdManager) Status() (string, error) {
	out, err := exec.Command("launchctl", "list", launchdLabel).Output()
	if err != nil {
		return "not loaded", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func (m *launchdManager) Restart() error {
	// The agent may live in either the gui domain (desktop login) or the
	// user domain (headless fallback) — kick whichever has it.
	guiTarget := "gui/" + currentUID() + "/" + launchdLabel
	stderr, err := runCaptureStderr("launchctl", "kickstart", "-k", guiTarget)
	if err == nil {
		return nil
	}
	userTarget := "user/" + currentUID() + "/" + launchdLabel
	if _, err2 := runCaptureStderr("launchctl", "kickstart", "-k", userTarget); err2 == nil {
		return nil
	}
	return fmt.Errorf("launchctl kickstart %s: %w: %s — is the service installed? (`dejima service install`)",
		guiTarget, err, strings.TrimSpace(stderr))
}

func currentUID() string {
	return fmt.Sprintf("%d", os.Getuid())
}

const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
{{- range .ProgramArguments}}
    <string>{{.}}</string>
{{- end}}
  </array>
  <key>WorkingDirectory</key>
  <string>{{.WorkingDir}}</string>
{{- if .UserName}}
  <key>UserName</key>
  <string>{{.UserName}}</string>
{{- end}}
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>{{.StdoutPath}}</string>
  <key>StandardErrorPath</key>
  <string>{{.StderrPath}}</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>HOME</key>
    <string>{{.Home}}</string>
  </dict>
</dict>
</plist>
`
