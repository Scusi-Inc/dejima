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

	// `launchctl bootstrap` is the modern verb; falls back to `load` on older macOS.
	if err := exec.Command("launchctl", "bootstrap", "gui/"+currentUID(), path).Run(); err != nil {
		if loadErr := exec.Command("launchctl", "load", "-w", path).Run(); loadErr != nil {
			return fmt.Errorf("launchctl bootstrap/load: %v / %v", err, loadErr)
		}
	}
	return nil
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
