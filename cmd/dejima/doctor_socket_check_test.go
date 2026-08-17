package main

import (
	"os/exec"
	"strings"
	"testing"
)

// socketRow returns the `host sockets` row, failing if the check emitted none.
// A missing row is itself the bug this file is about: silence reads exactly like
// a clean bill of health.
func socketRow(t *testing.T) doctorRow {
	t.Helper()
	r := &doctorReport{}
	checkHostSocketPressure(r)
	for _, row := range r.rows {
		if row.check == "host sockets" {
			return row
		}
	}
	t.Fatalf("no `host sockets` row emitted; rows=%+v", r.rows)
	return doctorRow{}
}

// When netstat isn't there, the check must SAY it didn't look — and must not
// dress that up as a healthy result.
//
// The two non-failures ("I looked, the host is fine" and "I never looked") are
// the ones most easily rendered identically, and rendering them identically is
// what let this check sit broken for days: an operator reading the report, or a
// reviewer reading a red test, both concluded "environmental, unrelated".
func TestHostSocketPressureUnavailableIsInfoNotOK(t *testing.T) {
	prev := netstatCommand
	t.Cleanup(func() { netstatCommand = prev })
	netstatCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("dejima-no-such-binary-for-tests")
	}

	row := socketRow(t)
	if row.status == "OK" {
		t.Errorf("an unmeasured check must not report OK — that manufactures a clean bill of health; got %+v", row)
	}
	if row.status != "INFO" {
		t.Errorf("status = %q, want INFO", row.status)
	}
	if !strings.Contains(row.detail, "not measured") {
		t.Errorf("detail must say the measurement didn't happen, got %q", row.detail)
	}
	if !strings.Contains(row.detail, "netstat") {
		t.Errorf("detail should name what's missing so it's actionable, got %q", row.detail)
	}
	// The failure mode with teeth: reporting a number nobody measured.
	if strings.Contains(row.detail, "TIME_WAIT") {
		t.Errorf("an unmeasured check must not report a socket count, got %q", row.detail)
	}
}

// The complement: when netstat DOES run, the row carries the real number —
// including when that number is zero, which is the healthiest reading and the
// one an operator watches over days.
func TestHostSocketPressureReportsTheMeasurement(t *testing.T) {
	prev := netstatCommand
	t.Cleanup(func() { netstatCommand = prev })
	netstatCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("printf", "%s\n%s\n",
			"tcp4  0  0  127.0.0.1.51234  127.0.0.1.7280  TIME_WAIT",
			"tcp4  0  0  127.0.0.1.51235  93.184.216.34.443  TIME_WAIT")
	}

	row := socketRow(t)
	if strings.Contains(row.detail, "not measured") {
		t.Fatalf("netstat ran, so the row must carry the measurement, got %q", row.detail)
	}
	if !strings.Contains(row.detail, "2 sockets in TIME_WAIT") {
		t.Errorf("detail should report the host-wide count, got %q", row.detail)
	}
	if !strings.Contains(row.detail, "(1 to the egress proxy)") {
		t.Errorf("detail should break out the proxy-bound count, got %q", row.detail)
	}
}

// Zero is a measurement, not an absence. This is the case the original test was
// written for and the reason the check must not bail early on an empty result.
func TestHostSocketPressureZeroIsAMeasurement(t *testing.T) {
	prev := netstatCommand
	t.Cleanup(func() { netstatCommand = prev })
	netstatCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("printf", "%s\n", "Active Internet connections (including servers)")
	}

	row := socketRow(t)
	if strings.Contains(row.detail, "not measured") {
		t.Fatalf("a healthy host is measured, not unmeasured: %q", row.detail)
	}
	if !strings.Contains(row.detail, "0 sockets in TIME_WAIT") {
		t.Errorf("a healthy host must still print its zero, got %q", row.detail)
	}
}

// A netstat that exists but fails is a third state, and must be reported as
// unmeasured too — not silently swallowed, and not counted as healthy.
func TestHostSocketPressureNetstatFailureIsUnmeasured(t *testing.T) {
	prev := netstatCommand
	t.Cleanup(func() { netstatCommand = prev })
	netstatCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo boom >&2; exit 3")
	}

	row := socketRow(t)
	if row.status == "OK" {
		t.Errorf("a failed netstat must not read as OK, got %+v", row)
	}
	if !strings.Contains(row.detail, "not measured") {
		t.Errorf("detail must say the measurement didn't happen, got %q", row.detail)
	}
}
