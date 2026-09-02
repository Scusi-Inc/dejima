package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/wsl"
)

// dejimadUnit is the systemd unit that keeps the daemon alive in a WSL distro.
//
// This is the exact text that was verified on a real machine: installed,
// enabled, `wsl --terminate`, distro rebooted, socket present and dejimad
// running with no window held open. Every line below earned its place by a
// failure that happened first, so read this before "simplifying" it.
//
//	Environment=HOME=/root
//	    THE ONE THAT COST THE MOST. A systemd system service starts with
//	    essentially no environment, and HOME is not among the few it sets. The
//	    daemon needs it to locate ~/.dejima and exits immediately without it:
//	        level=ERROR msg="dejimad fatal" err="locate home dir: $HOME is not defined"
//	    restarting every two seconds, forever.
//
//	/usr/local/bin/dejimad, absolute
//	    The unattended PATH excludes /usr/local/bin. The same defect first
//	    appeared in a wsl.conf boot command as
//	        setsid: failed to execute dejimad: No such file or directory
//
//	the stale-socket ExecStartPre
//	    dejimad refuses to bind over an existing socket, so one left by an
//	    unclean shutdown makes the unit fail permanently. startDaemonInWSL
//	    already cleared this in Go; not carrying it into the unit would have
//	    reintroduced it the first time the distro died badly. It only removes
//	    the socket when no dejimad is running, so it cannot shoot down a live one.
//
//	Restart=always
//	    The reason to prefer systemd over a boot command at all: it brings the
//	    daemon back by itself, and it gives the self-updater a working
//	    `systemctl restart` — the failure that started this whole investigation.
//
// The pattern behind all three failures is one thing: THE UNATTENDED CONTEXT
// LACKS WHAT EVERY INTERACTIVE CONTEXT SUPPLIES FOR FREE. `wsl -d <distro> --
// dejimad` has PATH and HOME and works every time, which is exactly why testing
// by hand cannot find any of this.
// dejimadUnitFor builds the unit, with the listener overrides a NATIVE docker
// engine needs.
//
// WHY THE OVERRIDES ARE HERE AND NOT IN THE DAEMON'S DEFAULTS. dejimad binds the
// egress proxy and the token-autonomy listener to 127.0.0.1, and tells containers
// to reach them at host.docker.internal. That works on Docker Desktop and colima
// because a VM sits in the middle and forwards the name to the host's loopback.
// A WSL distro runs a NATIVE engine — installDocker installs from get.docker.com
// — so there is no VM, host.docker.internal resolves to the bridge gateway, and
// A CONTAINER CANNOT REACH THE HOST'S LOOPBACK. The operator's first island
// failed with:
//
//	fatal: unable to access 'https://github.com/…': Failed to connect to
//	host.docker.internal port 7280 after 0 ms: Couldn't connect to server
//
// Fixing the DEFAULT is a daemon-wide change that affects every platform and is
// being done separately. This is the installer configuring the host it is
// installing on, which it can do precisely because it knows the host is WSL with
// a native engine.
//
// The bind stays HOST-INTERNAL: a bridge gateway is reachable from containers on
// that bridge and from the distro, not from the LAN. assertHostInternalBind
// already permits exactly this and refuses only wildcards — its own comment
// names "a docker bridge gateway" as the allowed case.
//
// When no gateway can be detected the overrides are omitted rather than guessed,
// leaving today's behaviour, and setup says so.
func dejimadUnitFor(bridgeGateway string) string {
	env := "Environment=HOME=/root\n"
	if bridgeGateway != "" {
		env += "Environment=DEJIMAD_EGRESS_PROXY=" + bridgeGateway + ":7280\n"
		env += "Environment=DEJIMAD_TOKEN_TCP=" + bridgeGateway + ":7274\n"
	}
	return strings.Replace(dejimadUnit, "Environment=HOME=/root\n", env, 1)
}

// distroBridgeGateway reports the docker bridge gateway inside the distro, or ""
// when it cannot be determined. docker0 specifically: that is what Docker maps
// host-gateway to by default, and host-gateway is what host.docker.internal
// resolves to inside a container.
func distroBridgeGateway(ctx context.Context, distro string) string {
	// ASK DOCKER FIRST, then fall back to the interface.
	//
	// The first version only ran `ip … docker0`, and on a FRESH distro that
	// returned nothing: docker0 does not exist until dockerd has actually
	// started, and setup probes moments after installing it. The overrides were
	// then correctly omitted rather than guessed — and the operator's first
	// clone on a clean machine failed with
	//
	//	Failed to connect to host.docker.internal port 7280
	//
	// I had verified the detection on a host where Docker had been running for
	// hours, which is the end state, not the state setup runs in. Same mistake
	// as everything else this week: the path nobody walks from nothing.
	//
	// `docker network inspect bridge` asks the daemon we just started, so it is
	// authoritative and it also answers on images with no iproute2. It is tried
	// with a short retry, because dockerd coming up is exactly the race that was
	// lost here.
	probes := []string{
		`docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null`,
		`ip -4 -o addr show docker0 2>/dev/null | awk '{print $4}' | cut -d/ -f1`,
	}
	for attempt := 0; attempt < 5; attempt++ {
		for _, probe := range probes {
			out, err := wsl.Run(ctx, distro, probe)
			if err != nil {
				continue
			}
			addr := strings.TrimSpace(out)
			// A plain IPv4 literal or nothing. A wrong bind is worse than none.
			if addr != "" && net.ParseIP(addr) != nil && !strings.Contains(addr, ":") {
				return addr
			}
		}
		if attempt < 4 {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(2 * time.Second):
			}
		}
	}
	return ""
}

const dejimadUnit = `[Unit]
Description=Dejima daemon
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
Environment=HOME=/root
ExecStartPre=/bin/mkdir -p /root/.dejima
ExecStartPre=/bin/sh -c 'pgrep -x dejimad >/dev/null 2>&1 || rm -f /root/.dejima/dejimad.sock'
ExecStart=/bin/sh -c 'exec /usr/local/bin/dejimad --foreground >>/root/.dejima/dejimad.log 2>&1'
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
`

// distroHasSystemd reports whether systemd is actually running as PID 1 in the
// distro — /run/systemd/system exists only when it is. Testing for the systemctl
// BINARY would be wrong: WSL images ship it with no init, which is how a
// `systemctl restart` came to be attempted, and fail, on a distro that had it
// disabled.
func distroHasSystemd(ctx context.Context, distro string) bool {
	_, err := wsl.Run(ctx, distro, "test -d /run/systemd/system")
	return err == nil
}

// ensureWSLDaemonSupervision makes the daemon survive the distro restarting.
//
// WSL tears a distro down with its last interop session, so a backgrounded
// process cannot outlive the command that started it — `nohup … &` and `setsid
// nohup … </dev/null &` were both tried in the field and both left nothing
// behind. Supervision has to come from inside the distro's own init.
func ensureWSLDaemonSupervision(ctx context.Context, distro string) (string, error) {
	if !distroHasSystemd(ctx, distro) {
		changed, err := ensureWSLBootCommand(ctx, distro)
		if err != nil {
			return "", err
		}
		if changed {
			return "wsl.conf boot command installed — the daemon starts with the distro", nil
		}
		return "", nil
	}

	// base64 for the same reason the wsl.conf write uses it: the unit contains
	// quotes, redirections and single-quoted sh fragments, and this text crosses
	// PowerShell, wsl.exe and sh. Hand-quoting it is how a command ends up
	// evaluated by the wrong shell — which happened to the operator on this very
	// file, with PowerShell swallowing a `>>` and writing to a Windows path.
	enc := b64(dejimadUnitFor(distroBridgeGateway(ctx, distro)))
	// RESTART, not just enable --now. `enable --now` starts a stopped unit and
	// leaves a running one alone, so rewriting the file for an already-running
	// daemon would change the config on disk and nothing else — the daemon would
	// keep its old listeners and the operator would see no change from a setup
	// that reported success.
	script := "echo " + enc + " | base64 -d > /etc/systemd/system/dejimad.service && " +
		"systemctl daemon-reload && systemctl enable dejimad && systemctl restart dejimad"
	if _, err := wsl.Run(ctx, distro, script); err != nil {
		return "", fmt.Errorf("install the dejimad systemd unit in %s: %w", distro, err)
	}
	return "systemd unit installed and enabled — the daemon starts with the distro and restarts on failure", nil
}

// unitIsCurrent reports whether the distro already has this exact unit, so setup
// can stay quiet on a re-run instead of announcing work it did not do.
func unitIsCurrent(ctx context.Context, distro string) bool {
	out, err := wsl.Run(ctx, distro, "cat /etc/systemd/system/dejimad.service 2>/dev/null || true")
	return err == nil && strings.TrimSpace(out) == strings.TrimSpace(dejimadUnitFor(distroBridgeGateway(ctx, distro)))
}

// b64/unb64 exist so the round-trip is testable as the thing that actually runs,
// rather than a restatement of it.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func unb64(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}
