package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// defaultEgressProxyPort mirrors dejimad's default egress listener port. Doctor
// runs client-side and can't import the daemon's main package, so it's repeated
// here; the daemon flag help documents the same value.
const defaultEgressProxyPort = "7280"

// checkEgressProxy probes the island egress proxy on the daemon host.
//
// Doctor previously said "all OK" through a total egress outage because it
// never looked at this listener — the one path every agent's LLM traffic
// takes. Two things are checked, because they fail differently:
//
//   - Does the listener accept? A daemon that is up but not accepting looks
//     identical to a healthy one from the outside: the client's SYN sits in a
//     backlog nobody drains and the connection just hangs.
//
//   - Is the host's socket table healthy? Every island connection to the proxy
//     is a host-loopback connection (via the Lima/Colima port-forwarder), and
//     each one leaves a TIME_WAIT behind. Enough of them exhaust the ephemeral
//     port range, after which new connections stall in SYN_SENT and egress
//     dies host-wide, with the daemon itself looking perfectly healthy.
//
// Skipped when not on the daemon host — the proxy binds loopback, so a remote
// client can't reach it and a failed dial would say nothing.
func checkEgressProxy(r *doctorReport) {
	_, _, source := resolveTarget()
	if source != "local" {
		return
	}

	addr := net.JoinHostPort("127.0.0.1", defaultEgressProxyPort)
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if c != nil {
		_ = c.Close()
	}
	disabled, known := egressProxyDisabled()
	level, detail, fix := egressListenerStatus(addr, err, disabled, known)
	r.add("Egress", "proxy listener", level, detail, fix)

	checkHostSocketPressure(r)
}

// checkHostSocketPressure reports ephemeral-port consumption on the daemon
// host. This is the resource that runs out first when island egress is proxied
// through host loopback, and nothing else in doctor would notice.
func checkHostSocketPressure(r *doctorReport) {
	total, toProxy, unavailable := timeWaitCounts()
	if unavailable != "" {
		// "Couldn't look" is reported, and reported as its own thing. Returning
		// silently here made a host where the check never ran indistinguishable
		// from a healthy one — the exact failure this check's own test was written
		// to catch. Reporting OK with a zero count would be worse still: that
		// manufactures a clean bill of health instead of merely implying one. So:
		// INFO, and the text says "not measured" and carries no number.
		r.add("Egress", "host sockets", "INFO",
			"not measured — "+unavailable,
			"doctor can't watch ephemeral-port pressure on this host until that's resolved; "+
				"the check is advisory, so nothing else is blocked")
		return
	}
	span, haveSpan := ephemeralPortSpan()

	detail := fmt.Sprintf("%d sockets in TIME_WAIT (%d to the egress proxy)", total, toProxy)
	if !haveSpan {
		r.add("Egress", "host sockets", "INFO", detail, "")
		return
	}
	detail += fmt.Sprintf("; ephemeral port range holds %d", span)

	switch pct := total * 100 / span; {
	case pct >= 90:
		r.add("Egress", "host sockets", "FAIL",
			detail+" — the ephemeral port range is exhausted, so new outbound connections cannot be assigned a port",
			"this clears as TIME_WAIT drains (~30s); if it does not drain, the host's socket table is wedged and needs a reboot. Run the daemon with --no-egress-proxy to stop routing island egress through host loopback")
	case pct >= 60:
		r.add("Egress", "host sockets", "WARN",
			detail+" — ephemeral ports are running low; egress will stall if this keeps climbing",
			"consider --no-egress-proxy if island egress volume is high")
	default:
		r.add("Egress", "host sockets", "OK", detail, "")
	}
}

// netstatCommand is the test seam for the netstat shell-out.
var netstatCommand = exec.Command

// timeWaitCounts returns the number of TIME_WAIT sockets host-wide and the
// number of those pointed at the egress proxy port.
//
// The third return is why the measurement couldn't be taken, or "" when it was.
// It is a REASON rather than a bool because the caller has to tell the operator
// which of two very different things happened — "I looked and the host is fine"
// versus "I never looked" — and a bare false collapses them.
func timeWaitCounts() (total, toProxy int, unavailable string) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return 0, 0, "host socket state isn't readable on " + runtime.GOOS
	}
	out, err := netstatCommand("netstat", "-an", "-p", "tcp").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return 0, 0, "netstat isn't installed on this host (Debian/Ubuntu: `apt install net-tools`)"
		}
		return 0, 0, fmt.Sprintf("netstat failed: %v", err)
	}
	proxySuffix := "." + defaultEgressProxyPort      // darwin: 127.0.0.1.7280
	proxySuffixLinux := ":" + defaultEgressProxyPort // linux: 127.0.0.1:7280

	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "TIME_WAIT") {
			continue
		}
		total++
		for _, f := range strings.Fields(line) {
			if strings.HasSuffix(f, proxySuffix) || strings.HasSuffix(f, proxySuffixLinux) {
				toProxy++
				break
			}
		}
	}
	// Reaching here means netstat ran, whatever it found. Zero sockets in
	// TIME_WAIT is the healthiest possible reading — treating it as "no data"
	// would hide the line precisely when the host is fine, and make a healthy
	// host indistinguishable from a check that never ran. This is the number an
	// operator watches over days to see churn returning.
	return total, toProxy, ""
}

// ephemeralPortSpan returns how many ephemeral ports the host can hand out.
func ephemeralPortSpan() (int, bool) {
	if runtime.GOOS != "darwin" {
		return 0, false
	}
	first, ok1 := sysctlInt("net.inet.ip.portrange.first")
	last, ok2 := sysctlInt("net.inet.ip.portrange.last")
	if !ok1 || !ok2 || last <= first {
		return 0, false
	}
	return last - first + 1, true
}

func sysctlInt(key string) (int, bool) {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return n, err == nil
}

// isTimeout reports whether err is a network timeout.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// egressProxyDisabled reports whether the running daemon was started with
// --no-egress-proxy, and whether that could be determined at all.
//
// There is no API for this, and reading the service file only covers one
// install mode — so inspect the live process's arguments, which is true for
// however the daemon was started (launchd, systemd, or by hand). When the
// answer is unknown the caller must not report a hard failure: a deliberate
// bypass and a dead proxy look identical from the outside.
func egressProxyDisabled() (disabled, known bool) {
	out, err := exec.Command("pgrep", "-x", "dejimad").Output()
	if err != nil {
		return false, false
	}
	pid := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if pid == "" {
		return false, false
	}
	args, err := exec.Command("ps", "-o", "command=", "-p", pid).Output()
	if err != nil {
		return false, false
	}
	return strings.Contains(string(args), "--no-egress-proxy"), true
}

// egressListenerStatus classifies a dial at the egress proxy into a doctor
// verdict. Pure so the decision table can be tested directly — the first cut of
// this check reported FAIL for a host deliberately running --no-egress-proxy,
// making doctor exit non-zero on a correctly configured machine.
//
// disabled/known come from the running daemon's arguments; when the intent is
// unknown, "refused" is ambiguous and must not be a hard failure.
func egressListenerStatus(addr string, err error, disabled, known bool) (level, detail, fix string) {
	switch {
	case err == nil && disabled:
		// Something holds the port that the running daemon should not have opened.
		return "WARN",
			addr + " — something is listening even though the daemon runs with --no-egress-proxy",
			"another dejimad or an unrelated process may hold the port"
	case err == nil:
		return "OK", addr + " — accepting connections", ""
	case disabled:
		// Deliberately off: not a failure. Doctor must not exit non-zero for a
		// correctly configured host — but name the trade-off, since the proxy is
		// where egress observability and allow/deny come from.
		return "INFO",
			"disabled — the daemon runs with --no-egress-proxy; island egress bypasses the proxy",
			"island outbound is unobserved and `dejima egress allow/deny` is unavailable; re-enable by reinstalling the service without --no-egress-proxy"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		// Not the daemon's fault and not fixed by restarting it: the host has no
		// ephemeral port left to originate from. Say so, because "can't assign
		// requested address" reads like a bind error.
		return "FAIL",
			addr + " — cannot allocate a local port to reach the proxy; the host is out of ephemeral ports",
			"see host sockets below — this is host socket exhaustion, not a dead daemon; restarting dejimad will not clear it"
	case isTimeout(err):
		// The outage signature: the handshake never completes, because the accept
		// backlog is not being drained or no ports remain to complete it.
		return "FAIL",
			addr + " — connection timed out (SYN sent, never accepted); island egress is down",
			"check host socket pressure below; restart the daemon, or reinstall the service with --no-egress-proxy to bypass the proxy"
	case errors.Is(err, syscall.ECONNREFUSED) && !known:
		return "WARN",
			addr + " — connection refused (nothing listening); could not determine whether the proxy is intentionally off",
			"if the daemon runs with --no-egress-proxy this is expected; otherwise check that dejimad is running"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "FAIL",
			addr + " — connection refused; the daemon should be running the proxy but nothing is listening",
			"restart the daemon: `dejima service restart --system`"
	default:
		return "FAIL", fmt.Sprintf("%s — %v", addr, err), "is dejimad running?"
	}
}
