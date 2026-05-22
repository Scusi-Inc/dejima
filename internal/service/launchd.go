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

func (m *launchdManager) Install(binaryPath string) error {
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

	tmpl := template.Must(template.New("plist").Parse(launchdTemplate))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{
		"Label":       launchdLabel,
		"Binary":      binaryPath,
		"WorkingDir":  home,
		"StdoutPath":  filepath.Join(logDir, "dejimad.out.log"),
		"StderrPath":  filepath.Join(logDir, "dejimad.err.log"),
	}); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}

	// Idempotent: clear any prior load before re-loading. Both commands are
	// expected to no-op if nothing's loaded; we ignore their errors.
	_ = exec.Command("launchctl", "bootout", "gui/"+currentUID()+"/"+launchdLabel).Run()
	_ = exec.Command("launchctl", "unload", path).Run()

	// Try the modern `bootstrap` first; if that fails (commonly when called
	// from an SSH session outside the Aqua/GUI session), fall back to the
	// older `load -w`. Capture stderr on each so we can surface useful errors.
	if stderr, err := runCaptureStderr("launchctl", "bootstrap", "gui/"+currentUID(), path); err != nil {
		if stderr2, err2 := runCaptureStderr("launchctl", "load", "-w", path); err2 != nil {
			return fmt.Errorf("launchctl bootstrap → %v (%s); load -w → %v (%s)",
				err, strings.TrimSpace(stderr), err2, strings.TrimSpace(stderr2))
		}
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
    <string>{{.Binary}}</string>
  </array>
  <key>WorkingDirectory</key>
  <string>{{.WorkingDir}}</string>
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
  </dict>
</dict>
</plist>
`
