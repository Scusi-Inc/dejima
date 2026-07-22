package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const launchdSystemPlist = "/Library/LaunchDaemons/" + launchdLabel + ".plist"

// launchdSystemSudoers is a narrowly-scoped sudoers drop-in that lets the daemon
// user apply self-updates without a password. sudo ignores files with a '.' in
// the name, so the basename is bare.
const launchdSystemSudoers = "/etc/sudoers.d/dejima"

// sudoersTemplate grants exactly the two privileged steps a daemon self-update
// needs and nothing else: replace the two dejima binaries in their install dir,
// and restart this one launchd job. The binary destinations and the restart
// target are pinned; only the copy's source path is a wildcard (sudo matches
// arguments positionally, so no extra flags can be smuggled in). Verbs:
// %[1]s=user, %[2]s=dejima path, %[3]s=dejimad path, %[4]s=launchd label.
const sudoersTemplate = `# Managed by dejima (dejima service install --system).
# Remove with 'dejima service uninstall' or by deleting this file.
#
# Lets %[1]s apply daemon self-updates (dejima update, TUI [U]) without a
# password prompt: the system daemon is headless (no TTY), so the two privileged
# steps — replacing the dejima binaries and restarting the launchd job — cannot
# prompt for one. Scope is deliberately narrow: the binary destinations and the
# restart target are pinned; only the source path of the copy is a wildcard.
%[1]s ALL=(root) NOPASSWD: /usr/bin/install -m 0755 * %[2]s, \
                          /usr/bin/install -m 0755 * %[3]s, \
                          /bin/launchctl kickstart -k system/%[4]s
`

// parseIDs converts a user's numeric uid/gid strings into ints for os.Chown.
func parseIDs(u *user.User) (uid, gid int, ok bool) {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, false
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, false
	}
	return uid, gid, true
}

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
	// `dejima service install --system` is run under sudo, so user.Current()
	// reports root. But the daemon must run as the human who invoked it: that's
	// who owns the Docker/colima socket, SSH keys, and login keychain it needs.
	// Recover the real account from $SUDO_USER. (Without this the daemon runs as
	// root with HOME=/var/root, sees none of the user's container runtime, and
	// island operations fail.)
	if u.Uid == "0" {
		if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
			if real, lerr := user.Lookup(sudoUser); lerr == nil {
				u = real
			}
		}
	}

	logDir := filepath.Join(u.HomeDir, "Library", "Logs", "dejima")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	// We're running as root here; the daemon runs as u. Hand the log dir (and
	// any parent dirs we just created) to u so it can write its own logs.
	if uid, gid, ok := parseIDs(u); ok {
		_ = os.Chown(logDir, uid, gid)
	}

	var buf bytes.Buffer
	if err := renderPlist(&buf, map[string]any{
		"Label":            launchdLabel,
		"ProgramArguments": append([]string{binaryPath}, args...),
		"WorkingDir":       u.HomeDir,
		"Home":             u.HomeDir,
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

	// Drop a passwordless self-update sudoers rule so `dejima update` / TUI [U] can
	// update this headless daemon with no TTY. Best-effort: the daemon is fine
	// without it; self-update just falls back to a manual sudo. binDir is where the
	// binaries live (and where make install puts them) — the dir of the daemon path
	// we just wrote into the plist.
	if err := installSelfUpdateSudoers(u, filepath.Dir(binaryPath)); err != nil {
		fmt.Printf("note: passwordless self-update not enabled (%v)\n", err)
		fmt.Println("      `dejima update` will need a manual sudo; the daemon itself is unaffected.")
	} else {
		fmt.Printf("installed %s — `dejima update` / TUI [U] can self-update the daemon without a password\n", launchdSystemSudoers)
	}

	_ = exec.Command("sudo", "launchctl", "bootout", "system/"+launchdLabel).Run()
	// bootout returns before launchd finishes tearing down the old job;
	// bootstrapping the same label mid-teardown fails with EIO ("Bootstrap
	// failed: 5: Input/output error"). Wait for the label to vanish, then
	// retry the bootstrap a few times for good measure.
	for i := 0; i < 20; i++ {
		if exec.Command("launchctl", "print", "system/"+launchdLabel).Run() != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	var stderr string
	var err2 error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		stderr, err2 = runCaptureStderr("sudo", "launchctl", "bootstrap", "system", launchdSystemPlist)
		if err2 == nil {
			return nil
		}
	}
	return fmt.Errorf("launchctl bootstrap system: %w: %s", err2, strings.TrimSpace(stderr))
}

func (m *launchdSystemManager) Uninstall() error {
	// Bounded: an unbounded bootout deadlocks when the caller is inside the job
	// being torn down (see runTeardown).
	if err := runTeardown("sudo", "launchctl", "bootout", "system/"+launchdLabel); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	// Retire the passwordless self-update rule alongside the daemon (best-effort).
	_ = runTeardown("sudo", "rm", "-f", launchdSystemSudoers)
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

// installSelfUpdateSudoers writes the scoped /etc/sudoers.d/dejima drop-in (see
// sudoersTemplate) so the headless daemon can self-update with no TTY. It
// validates with `visudo -cf` first and never installs an unparseable file — a
// broken sudoers drop-in can wedge sudo for every user on the box.
func installSelfUpdateSudoers(u *user.User, binDir string) error {
	content := fmt.Sprintf(sudoersTemplate,
		u.Username,
		filepath.Join(binDir, "dejima"),
		filepath.Join(binDir, "dejimad"),
		launchdLabel,
	)
	tmp, err := os.CreateTemp("", "dejima-sudoers-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if stderr, err := runCaptureStderr("/usr/sbin/visudo", "-cf", tmp.Name()); err != nil {
		return fmt.Errorf("validate sudoers: %w: %s", err, strings.TrimSpace(stderr))
	}
	// sudo ignores a drop-in that isn't 0440 root:wheel. We may not be root (the
	// caller routes privileged steps through sudo), so do the same here.
	if stderr, err := runCaptureStderr("sudo", "install", "-m", "0440", "-o", "root", "-g", "wheel",
		tmp.Name(), launchdSystemSudoers); err != nil {
		return fmt.Errorf("install %s: %w: %s", launchdSystemSudoers, err, strings.TrimSpace(stderr))
	}
	return nil
}
