package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/aoos/dejima/internal/wsl"
)

// wslBootCommand is what WSL runs every time it boots the distro.
//
// This is the WSL equivalent of a service manager, and it is the only mechanism
// that actually works here. Everything else was tried in the field first:
// `nohup … &` died with the wsl.exe session, and `setsid nohup … </dev/null &`
// died too — verified on a real machine, socket and process both gone the
// moment the window closed. WSL terminates a distro when its last interop
// session exits, so a background process inside it cannot outlive the thing
// that started it, no matter how thoroughly it is detached.
//
// A boot command inverts that instead of fighting it. The distro is allowed to
// come and go; whenever anything touches it — including the client's own socat
// dial — WSL boots it and this runs, so the daemon is there by the time the
// connection is made. It survives `wsl --shutdown`, a Windows reboot, and the
// distro idling out, none of which the previous approach did.
// The path is ABSOLUTE. The boot context is not a login shell and its PATH does
// not include /usr/local/bin, so a bare `dejimad` resolves to nothing:
//
//	setsid: failed to execute dejimad: No such file or directory
//
// which is what the field log showed — the command ran exactly as configured and
// found nothing to run. An interactive `wsl -d <distro> -- dejimad` works, which
// is precisely why this was easy to get wrong: the binary is on PATH in every
// context a person tests by hand, and absent in the one that matters.
const wslBootCommand = `setsid /usr/local/bin/dejimad --foreground >>/root/.dejima/dejimad.log 2>&1`

// mergeWSLConf adds (or updates) the [boot] command in an existing wsl.conf,
// leaving every other section and key untouched.
//
// Rewriting the file wholesale would be easier and is wrong: wsl.conf commonly
// carries [user] default=, [network] hostname=, [interop] settings — things the
// operator set and that Dejima has no business discarding to install a daemon.
// A setup step that silently resets someone's distro configuration is a worse
// bug than the one it fixes.
func mergeWSLConf(existing, command string) string {
	want := `command = "` + command + `"`
	lines := strings.Split(existing, "\n")

	var out []string
	inBoot := false
	replaced := false
	sawBoot := false

	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			// Leaving [boot] without having written a command means the section
			// existed but had none; add it before moving on.
			if inBoot && !replaced {
				out = append(out, want)
				replaced = true
			}
			inBoot = strings.EqualFold(t, "[boot]")
			if inBoot {
				sawBoot = true
			}
			out = append(out, ln)
			continue
		}
		// Replace an existing command inside [boot] rather than appending a
		// second one — WSL takes the last, so a duplicate would work by accident
		// and confuse anyone reading the file.
		if inBoot && strings.HasPrefix(strings.ToLower(t), "command") && strings.Contains(t, "=") {
			if !replaced {
				out = append(out, want)
				replaced = true
			}
			continue
		}
		out = append(out, ln)
	}
	// A [boot] section that ran to the end of the file without a command. Drop
	// the trailing blank lines the file's final newline produces, or the key
	// lands after a gap — still inside the section and still valid, but it reads
	// like it belongs to nothing, in a file the operator will open again.
	if inBoot && !replaced {
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, want)
	}

	body := strings.Join(out, "\n")
	if !sawBoot {
		if strings.TrimSpace(body) != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "\n[boot]\n" + want + "\n"
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	// Collapse the blank-line run a prepend/append can leave behind.
	for strings.Contains(body, "\n\n\n") {
		body = strings.ReplaceAll(body, "\n\n\n", "\n\n")
	}
	return strings.TrimLeft(body, "\n")
}

// ensureWSLBootCommand installs the boot command in the distro's /etc/wsl.conf,
// preserving whatever else is in there. Reports whether the file changed, so a
// caller only pays the cost of restarting the distro when there is a reason to.
//
// The file is written via base64 rather than a quoted heredoc or printf. The
// content contains quotes, redirections and a path, and this repo has already
// paid for shell-quoting mistakes across this exact boundary more than once —
// the WSL channel ate variables out of script text and left the quotes intact,
// which fails toward looking fine. Bytes in, bytes out, nothing to interpret.
func ensureWSLBootCommand(ctx context.Context, distro string) (bool, error) {
	current, err := wsl.Run(ctx, distro, "cat /etc/wsl.conf 2>/dev/null || true")
	if err != nil {
		return false, fmt.Errorf("read /etc/wsl.conf in %s: %w", distro, err)
	}
	merged := mergeWSLConf(current, wslBootCommand)
	if strings.TrimSpace(merged) == strings.TrimSpace(current) {
		return false, nil
	}
	enc := base64.StdEncoding.EncodeToString([]byte(merged))
	// Try direct first, then sudo — the distro usually runs as root and need not
	// have sudo installed at all. Same shape as the socat and daemon installs.
	// Both branches must DECODE. An earlier draft of the sudo half piped the
	// base64 straight into tee, which would have written the encoded text into
	// wsl.conf — a file WSL then parses as config, so the breakage would have
	// shown up as a distro that boots wrong rather than as a failed write.
	script := "printf %s " + enc + " | base64 -d > /etc/wsl.conf 2>/dev/null || " +
		"printf %s " + enc + " | base64 -d | sudo -n tee /etc/wsl.conf >/dev/null"
	if _, err := wsl.Run(ctx, distro, script); err != nil {
		return false, fmt.Errorf("write /etc/wsl.conf in %s: %w", distro, err)
	}
	return true, nil
}
