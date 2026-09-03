package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/selfupdate"
	"github.com/aoos/dejima/internal/service"
	"github.com/aoos/dejima/internal/version"
	"github.com/aoos/dejima/internal/wsl"
)

// checkWSLHost reports the health of the WSL2 host on Windows — the only way to
// run a daemon locally there. It's a no-op everywhere else, and stays quiet on a
// Windows box that has deliberately chosen a remote server (no WSL profile, no
// WSL installed): the point is to diagnose a WSL host someone is trying to use,
// not to nag every Windows client toward one.
func checkWSLHost(ctx context.Context, r *doctorReport) {
	if !wsl.Supported() {
		return
	}
	host, _, _ := resolveTarget()
	targeted := wsl.IsHost(host)
	if !targeted {
		if !wsl.Available() {
			return // no WSL, not targeting it — nothing to say
		}
		// WSL exists but isn't the target. One INFO line, since a Windows user
		// staring at an unreachable "local" needs to know this door exists.
		if host == "" {
			r.add("Connection", "wsl", "INFO",
				"Windows can't run dejimad; WSL2 is available on this machine",
				"run a local host there: dejima wsl setup")
		}
		return
	}
	if !wsl.Available() {
		r.add("Connection", "wsl", "FAIL",
			"the active target is "+host+" but wsl.exe isn't available",
			"install WSL (admin PowerShell): "+wsl.InstallHint)
		return
	}
	rep, err := wsl.Probe(ctx, wsl.Distro(host))
	if err != nil {
		r.add("Connection", "wsl", "WARN", "couldn't inspect the distro: "+err.Error(), "dejima wsl status")
		return
	}
	switch {
	case !rep.Exists:
		r.add("Connection", "wsl", "FAIL",
			fmt.Sprintf("distro %q doesn't exist", rep.Distro), "dejima wsl setup")
	case rep.Version == 1:
		r.add("Connection", "wsl", "FAIL",
			fmt.Sprintf("distro %q is WSL 1 — no kernel, so Docker can't run", rep.Distro),
			fmt.Sprintf("wsl --set-version %s 2", rep.Distro))
	case !rep.HasSocat:
		r.add("Connection", "wsl", "FAIL",
			"socat isn't installed in the distro — the client tunnels the daemon socket through it",
			"dejima wsl setup")
	case !rep.HasDejima:
		r.add("Connection", "wsl", "FAIL", "dejimad isn't installed in the distro", "dejima wsl setup")
	case !rep.HasDocker:
		r.add("Connection", "wsl", "FAIL",
			"the Docker engine isn't responding in the distro — islands can't start",
			fmt.Sprintf("wsl -d %s -- sudo service docker start", rep.Distro))
	case !rep.SocketUp:
		r.add("Connection", "wsl", "FAIL", "dejimad isn't running in the distro", "dejima wsl start")
	default:
		r.add("Connection", "wsl", "OK",
			fmt.Sprintf("distro %q: WSL2, Docker up, dejimad running", rep.Distro), "")
	}
}

// checkConnection reports where the client connects and why, and offers to fix a
// saved profile whose host lost its port (the bug that silently dials :80).
func checkConnection(r *doctorReport) {
	host, label, source := resolveTarget()
	switch source {
	case "local":
		r.add("Connection", "target", "OK", "local unix socket", "")
	case "profile":
		r.add("Connection", "target", "OK", fmt.Sprintf("%s (profile %q)", host, label), "")
	case "env":
		cfg, _ := clientcfg.Load()
		if h, ok := cfg.ActiveHost(); ok && h != host {
			r.add("Connection", "target", "WARN",
				fmt.Sprintf("$DEJIMA_HOST=%s is overriding your saved profile %q (%s)", host, cfg.ActiveProfile, h),
				"unset DEJIMA_HOST to use the profile, or point them at the same address")
		} else {
			r.add("Connection", "target", "OK", host+" (from $DEJIMA_HOST)", "")
		}
	}

	// A saved profile with a port-less host would dial :80 — fixable in place.
	cfg, _ := clientcfg.Load()
	for _, p := range cfg.Profiles {
		// `wsl://<distro>` has no port by design; normalizeHost leaves it alone,
		// so it can never trip this check — the guard is belt-and-braces against a
		// hand-edited client.json.
		if p.Host == "" || wsl.IsHost(p.Host) || normalizeHost(p.Host) == p.Host {
			continue
		}
		name := p.Name
		r.addRepair("Connection", "profile "+name, "WARN",
			fmt.Sprintf("host %q has no port — would dial :80", p.Host),
			"append the default :7273",
			func() (string, error) { return fixProfilePort(name) })
	}
}

func fixProfilePort(name string) (string, error) {
	cfg, err := clientcfg.Load()
	if err != nil {
		return "", err
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == name {
			old := cfg.Profiles[i].Host
			cfg.Profiles[i].Host = normalizeHost(old)
			if err := clientcfg.Save(cfg); err != nil {
				return "", err
			}
			return fmt.Sprintf("%s → %s", old, cfg.Profiles[i].Host), nil
		}
	}
	return "", fmt.Errorf("profile %q no longer present", name)
}

// checkInstallMeta verifies a source-installed daemon can self-update — it needs
// a readable record of its git checkout. A common break: the meta was written
// root-owned (under sudo) and the daemon, running as the user, can't read it.
func checkInstallMeta(r *doctorReport) {
	if selfupdate.DetectMode() != selfupdate.ModeSource {
		return // a release/client install self-updates by download, not a checkout
	}
	if meta, err := selfupdate.LoadInstallMeta(); err == nil && meta.SourceDir != "" {
		if _, statErr := os.Stat(filepath.Join(meta.SourceDir, "go.mod")); statErr == nil {
			r.add("Connection", "self-update", "OK", "checkout recorded: "+meta.SourceDir, "")
			return
		}
	}
	r.addRepair("Connection", "self-update", "WARN",
		"source install has no usable recorded checkout — `U` / daemon self-update will fail",
		"record the checkout (run from your dejima tree)",
		fixInstallMeta)
}

func fixInstallMeta() (string, error) {
	cwd, _ := os.Getwd()
	dir, err := selfupdate.FindCheckout(cwd)
	if err != nil {
		return "", fmt.Errorf("can't find the dejima checkout from here — `cd <your dejima tree> && dejima doctor --fix`")
	}
	system := service.Detect().Mode == "launchd-system"
	if m, e := selfupdate.LoadInstallMeta(); e == nil && m.SourceDir != "" {
		system = m.System // trust a readable prior record
	}
	if p, e := paths.DaemonInstallMetaPath(); e == nil {
		_ = os.Remove(p) // we own ~/.dejima, so a stale root-owned file unlinks fine
	}
	if err := selfupdate.SaveInstallMeta(selfupdate.InstallMeta{SourceDir: dir, System: system}); err != nil {
		return "", err
	}
	return "recorded checkout " + dir, nil
}

// checkStateOwnership flags files in ~/.dejima owned by another user (root, from
// a sudo install) that the daemon — running as you — can't read. Report-only:
// the chown needs root, and the one file we can repair without sudo (the
// install-meta) is handled by checkInstallMeta.
func checkStateOwnership(r *doctorReport) {
	root, err := paths.Root()
	if err != nil {
		return
	}
	metaName := ""
	if p, e := paths.DaemonInstallMetaPath(); e == nil {
		metaName = filepath.Base(p)
	}
	var names []string
	for _, p := range rootOwnedStateFiles(root) {
		if filepath.Base(p) == metaName {
			continue // checkInstallMeta repairs this one without sudo
		}
		names = append(names, filepath.Base(p))
	}
	if len(names) == 0 {
		return
	}
	r.add("Connection", "state ownership", "WARN",
		"files the daemon can't read (root-owned, from a sudo install): "+strings.Join(names, ", "),
		fmt.Sprintf("hand them back: `sudo chown -R $(id -un) %s`", root))
}

// checkListenerExposure is a security-posture line: confirm the in-island
// autonomy listener is bound host-internal (loopback), not somewhere the LAN can
// reach it. Reads the system daemon's plist (world-readable); only meaningful for
// a launchd-system install.
func checkListenerExposure(r *doctorReport) {
	if service.Detect().Mode != "launchd-system" {
		return
	}
	args, err := systemDaemonArgs()
	if err != nil {
		return
	}
	switch tokenBind := flagValue(args, "--token-tcp"); {
	case tokenBind == "":
		r.add("Security", "autonomy listener", "INFO", "token-TCP off (no in-island autonomy path)", "")
	case isLoopbackAddr(tokenBind):
		r.add("Security", "autonomy listener", "OK", tokenBind+" — host-internal (loopback)", "")
	default:
		r.add("Security", "autonomy listener", "WARN",
			tokenBind+" — the bearer-token listener is NOT loopback; it's reachable beyond this host",
			"rebind host-internal: `dejima service install --system --token-tcp 127.0.0.1:7274 …`")
	}
	if tcp := flagValue(args, "--tcp"); tcp != "" {
		r.add("Security", "operator API", "OK", tcp+" — tailnet peers only (non-tailnet sources refused)", "")
	}
}

// systemDaemonArgs returns the ProgramArguments of the system LaunchDaemon plist.
func systemDaemonArgs() ([]string, error) {
	data, err := os.ReadFile("/Library/LaunchDaemons/dev.dejima.dejimad.plist")
	if err != nil {
		return nil, err
	}
	return parseProgramArguments(data)
}

// parseProgramArguments pulls the ordered <string> values of a plist's
// ProgramArguments array out of raw plist bytes.
func parseProgramArguments(data []byte) ([]string, error) {
	block := regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>(.*?)</array>`).FindSubmatch(data)
	if block == nil {
		return nil, fmt.Errorf("no ProgramArguments in plist")
	}
	var args []string
	for _, m := range regexp.MustCompile(`<string>(.*?)</string>`).FindAllSubmatch(block[1], -1) {
		args = append(args, string(m[1]))
	}
	return args, nil
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func isLoopbackAddr(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// skewFinding is a version-skew / liveness diagnosis for one island, ready to add
// to the report. status "" means nothing to report (the island is level/unknown
// and healthy). It's split out, pure, so the comparison logic is unit-tested
// without a daemon.
type skewFinding struct {
	status string // WARN | "" (nothing to report)
	detail string
	fix    string
}

// diagnoseIslandSkew compares one island's version stamp against the running
// daemon and folds in the zero-heartbeat liveness flag. The two are reported as a
// single finding because they have the same remedy — recreate the island against
// the current image — which is exactly what would have healed the motivating
// incident (a stale socket→TCP notify shim that silently never heart-beat).
//
//   - A stamp behind the daemon is the loud skew signal, with the exact remedy
//     inline: `island "x" built on v0.1.4, daemon on v0.5.3 — run: dejima upgrade x`.
//   - NeverHeardFrom on its own (stamp level/unknown) still warrants the upgrade:
//     a level island whose heartbeat never fired has a broken in-island hook, and
//     an upgrade re-derives the managed shims.
//
// daemonVer is the daemon's reported version; a non-release daemon ("dev", a
// git-describe build) can't be ordered, so skew comparison is skipped and only
// the heartbeat flag (which needs no version math) can fire.
func diagnoseIslandSkew(info api.IslandInfo, daemonVer string) skewFinding {
	stamp := info.UpgradedVersion
	if stamp == "" {
		stamp = info.BuiltVersion
	}

	behind := false
	if version.IsRelease(daemonVer) && version.IsRelease(stamp) {
		behind = version.Compare(stamp, daemonVer) < 0
	}

	switch {
	case behind:
		return skewFinding{
			status: "WARN",
			detail: fmt.Sprintf("built on %s, daemon on %s — stale island image (its /opt shims may be old)", stamp, daemonVer),
			fix:    fmt.Sprintf("dejima upgrade %s", info.Name),
		}
	case info.NeverHeardFrom:
		return skewFinding{
			status: "WARN",
			detail: "no agent-state heartbeat since boot — its in-island event hook looks broken (mail-nudges / idle-hibernate / idle metric are dark)",
			fix:    fmt.Sprintf("dejima upgrade %s (re-derives the managed hook shims)", info.Name),
		}
	default:
		return skewFinding{}
	}
}

// diagnoseOrphanReaping reports an island whose container has no init as PID 1,
// so nothing reaps a process whose parent exited first.
//
// The daemon passes --init now, which means this cannot be answered from the
// daemon's own source: every island looks fine there. It is a create-time
// property, so a container made before that flag keeps leaking a zombie per
// orphaned process for its entire life — and islands are long-lived by design.
// The island where this was found was 95% dead processes.
//
// WARN rather than FAIL: nothing is broken right now, and on a default docker
// (pid_max in the millions, no pids cgroup limit) exhaustion is far away. What
// it costs immediately is that `ps` inside the island becomes useless, which
// bites whoever is debugging something else — and anyone running with
// --pids-limit hits the wall much sooner.
//
// reaps is three-state. nil means the runtime could not be asked, and that gets
// an INFO row carrying no verdict rather than silence: a check that quietly
// didn't run must not look like one that passed.
func diagnoseOrphanReaping(reaps *bool, island string) skewFinding {
	if reaps == nil {
		return skewFinding{
			status: "INFO",
			detail: "couldn't determine whether this container reaps orphaned processes",
		}
	}
	if *reaps {
		return skewFinding{}
	}
	return skewFinding{
		status: "WARN",
		detail: "no init as PID 1 — orphaned processes become zombies and are never reaped " +
			"(container predates --init)",
		fix: "dejima upgrade " + island + " (recreates the container; it cannot be fixed in place)",
	}
}
