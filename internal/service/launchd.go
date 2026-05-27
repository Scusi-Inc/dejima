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
		"StdoutPath":       outLog,
		"StderrPath":       errLog,
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
	// from an SSH session with no Aqua/GUI session), fall back to the older
	// `load -w`. Capture stderr on each so we can surface useful errors.
	//
	// If both fail (headless Mac — no console login yet), we leave the plist
	// in place and warn loudly rather than erroring out. The plist will load
	// next time someone logs into the desktop; the caller is expected to
	// start `dejimad` manually for the current session.
	if stderr, err := runCaptureStderr("launchctl", "bootstrap", "gui/"+currentUID(), path); err != nil {
		if stderr2, err2 := runCaptureStderr("launchctl", "load", "-w", path); err2 != nil {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintf(os.Stderr, "warning: plist written to %s, but launchctl couldn't load it.\n", path)
			fmt.Fprintf(os.Stderr, "  bootstrap → %v: %s\n", err, strings.TrimSpace(stderr))
			fmt.Fprintf(os.Stderr, "  load -w   → %v: %s\n", err2, strings.TrimSpace(stderr2))
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "Likely cause: no Aqua/GUI session is active on this Mac (headless setup).")
			fmt.Fprintln(os.Stderr, "LaunchAgents need a logged-in user session. Two ways forward:")
			fmt.Fprintln(os.Stderr, "  • Run dejimad manually for now (won't persist across reboots):")
			fmt.Fprintf(os.Stderr, "      nohup %s \\\n", strings.Join(append([]string{binaryPath}, args...), " "))
			fmt.Fprintf(os.Stderr, "        > %s \\\n", outLog)
			fmt.Fprintf(os.Stderr, "        2> %s < /dev/null &\n", errLog)
			fmt.Fprintln(os.Stderr, "      disown")
			fmt.Fprintln(os.Stderr, "  • Log into the Mac's desktop once (Screen Sharing or physical),")
			fmt.Fprintln(os.Stderr, "    then `dejima service install` will load the plist correctly.")
			fmt.Fprintln(os.Stderr, "    Enable auto-login for the host in System Settings → Users & Groups")
			fmt.Fprintln(os.Stderr, "    so the GUI session survives reboots.")
			fmt.Fprintln(os.Stderr)
			return nil // soft failure — plist is in place, user just needs to start dejimad
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
{{- range .ProgramArguments}}
    <string>{{.}}</string>
{{- end}}
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
