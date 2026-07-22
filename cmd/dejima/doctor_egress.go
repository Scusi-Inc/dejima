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
	switch {
	case err == nil:
		_ = c.Close()
		r.add("Egress", "proxy listener", "OK", addr+" — accepting connections", "")
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		// Not the daemon's fault and not fixed by restarting it: the host has
		// no ephemeral port left to originate the connection from. Say so,
		// because "can't assign requested address" reads like a bind error.
		r.add("Egress", "proxy listener", "FAIL",
			addr+" — cannot allocate a local port to reach the proxy; the host is out of ephemeral ports",
			"see host sockets below — this is host socket exhaustion, not a dead daemon; restarting dejimad will not clear it")
	case isTimeout(err):
		// The distinguishing symptom: the TCP handshake never completes. Either
		// the accept backlog is not being drained or the host has no ephemeral
		// ports left to complete a connection with.
		r.add("Egress", "proxy listener", "FAIL",
			addr+" — connection timed out (SYN sent, never accepted); island egress is down",
			"check host socket pressure below; restart the daemon, or start it with --no-egress-proxy to bypass the proxy entirely")
	case errors.Is(err, syscall.ECONNREFUSED):
		r.add("Egress", "proxy listener", "FAIL",
			addr+" — connection refused (nothing listening)",
			"is dejimad running? if the proxy is intentionally off (--no-egress-proxy) this is expected")
	default:
		r.add("Egress", "proxy listener", "FAIL",
			fmt.Sprintf("%s — %v", addr, err),
			"is dejimad running? if the proxy is disabled this is expected (--no-egress-proxy)")
	}

	checkHostSocketPressure(r)
}

// checkHostSocketPressure reports ephemeral-port consumption on the daemon
// host. This is the resource that runs out first when island egress is proxied
// through host loopback, and nothing else in doctor would notice.
func checkHostSocketPressure(r *doctorReport) {
	total, toProxy, ok := timeWaitCounts()
	if !ok {
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

// timeWaitCounts returns the number of TIME_WAIT sockets host-wide and the
// number of those pointed at the egress proxy port. Best-effort: shells out to
// netstat, and reports ok=false wherever that isn't available or parseable.
func timeWaitCounts() (total, toProxy int, ok bool) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return 0, 0, false
	}
	out, err := exec.Command("netstat", "-an", "-p", "tcp").Output()
	if err != nil {
		return 0, 0, false
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
	return total, toProxy, total > 0
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
