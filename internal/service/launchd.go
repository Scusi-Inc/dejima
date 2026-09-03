package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/aoos/dejima/internal/fdlimit"
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

	var buf bytes.Buffer
	if err := renderPlist(&buf, map[string]any{
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

	// The GUI domain is where LaunchAgents normally live and where launchd
	// auto-loads them on desktop login. The per-user `user/<uid>` domain is the
	// headless fallback: still supervised (KeepAlive restarts) for this boot,
	// but torn down at shutdown with nothing to re-bootstrap it until someone
	// logs in — so not reboot-durable. For that, point at the system
	// LaunchDaemon. `load -w` remains a last resort for old launchctl versions.
	//
	// Probe for a GUI (Aqua) domain before trying to bootstrap into it. On a
	// headless Mac — SSH, no desktop login — that domain doesn't exist and the
	// attempt fails with "125: Domain does not support specified action", which
	// reads like a broken install rather than "nobody is logged in". Ask first,
	// and if it's absent skip straight to the user domain with an explanation.
	var stderrGUI string
	errGUI := errNoGUIDomain
	if guiDomainAvailable() {
		stderrGUI, errGUI = runCaptureStderr("launchctl", "bootstrap", "gui/"+currentUID(), path)
		if errGUI == nil {
			return nil
		}
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

// teardownTimeout bounds a service-teardown command.
//
// `launchctl bootout` is synchronous: it does not return until launchd has torn
// the job down, which means waiting for every process launchd associates with
// it. The daemon's descendants include host-terminal tmux servers — so an
// operator running an uninstall FROM one of those shells is inside the job
// being removed, and the wait is mutual: launchctl waits for the tmux tree,
// which contains the launchctl. Neither ever finishes.
//
// preflightNotInsideDaemon catches the common shape of this before we start.
// The timeout is the backstop for shapes it can't see, so the worst case is a
// reported failure rather than a wedged terminal.
const teardownTimeout = 90 * time.Second

// runTeardown runs a teardown command under teardownTimeout, reporting a
// timeout distinctly so the caller can explain it rather than appearing stuck.
func runTeardown(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out after %s — launchd is likely waiting on a process this "+
			"command is itself inside (a host terminal?). Run the uninstall from a plain shell on the host",
			name, teardownTimeout)
	}
	if err != nil && strings.TrimSpace(stderr.String()) != "" {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return err
}

func (m *launchdManager) Uninstall() error {
	path, err := m.plistPath()
	if err != nil {
		return err
	}
	// Best-effort, but bounded: see runTeardown on why an unbounded bootout can
	// deadlock against the caller.
	_ = runTeardown("launchctl", "bootout", "gui/"+currentUID()+"/"+launchdLabel)
	_ = runTeardown("launchctl", "bootout", "user/"+currentUID()+"/"+launchdLabel)
	_ = runTeardown("launchctl", "unload", path)
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

// errNoGUIDomain marks "we never attempted the gui domain" so the diagnostic
// below can say that, instead of reporting an exec error that never happened.
var errNoGUIDomain = errors.New("no GUI session on this Mac (nobody logged in at the console)")

// guiDomainAvailable reports whether launchd has an Aqua/GUI domain for this
// user — i.e. whether someone is logged in at the desktop. Asking the domain
// (no label) is the same mechanism Detect() already uses one level deeper.
func guiDomainAvailable() bool {
	return exec.Command("launchctl", "print", "gui/"+currentUID()).Run() == nil
}

func currentUID() string {
	return fmt.Sprintf("%d", os.Getuid())
}

// renderPlist executes the shared plist template into w. Both the per-user
// LaunchAgent and the system LaunchDaemon go through here so neither can drift
// from the other on things like the file-descriptor limit.
func renderPlist(w io.Writer, data map[string]any) error {
	if _, ok := data["NumberOfFiles"]; !ok {
		data["NumberOfFiles"] = fdlimit.Target
	}
	return template.Must(template.New("plist").Parse(launchdTemplate)).Execute(w, data)
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
  <!-- launchd's default soft limit is 256 files, which a few concurrent island
       egress tunnels exhaust; the daemon then hangs accepts instead of erroring.
       The daemon also raises this itself at startup — this covers the gap for
       anything that reads the limit before that runs. -->
  <key>SoftResourceLimits</key>
  <dict>
    <key>NumberOfFiles</key>
    <integer>{{.NumberOfFiles}}</integer>
  </dict>
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
