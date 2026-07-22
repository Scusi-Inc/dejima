package main

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

// The first cut of the egress check reported FAIL when the operator had
// deliberately run the daemon with --no-egress-proxy, so `dejima doctor` exited
// non-zero on a correctly configured host. Intent has to be part of the
// verdict, and "we couldn't tell" must never be a hard failure.
func TestEgressListenerStatus(t *testing.T) {
	const addr = "127.0.0.1:7280"
	refused := &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	noPort := &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EADDRNOTAVAIL)}
	timedOut := &net.OpError{Op: "dial", Err: &timeoutErr{}}

	for _, tc := range []struct {
		name      string
		err       error
		disabled  bool
		known     bool
		wantLevel string
	}{
		{"listening and expected", nil, false, true, "OK"},
		{"listening but daemon says disabled", nil, true, true, "WARN"},
		{"refused, deliberately disabled", refused, true, true, "INFO"},
		{"refused, proxy expected on", refused, false, true, "FAIL"},
		{"refused, intent unknown", refused, false, false, "WARN"},
		{"host out of ephemeral ports", noPort, false, true, "FAIL"},
		{"handshake never completes", timedOut, false, true, "FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			level, detail, _ := egressListenerStatus(addr, tc.err, tc.disabled, tc.known)
			if level != tc.wantLevel {
				t.Errorf("level = %q, want %q (detail: %s)", level, tc.wantLevel, detail)
			}
			if detail == "" {
				t.Error("detail is empty")
			}
		})
	}
}

// A deliberately disabled proxy must not be able to fail the doctor run,
// whatever dial error the probe happened to produce.
func TestEgressDisabledNeverFails(t *testing.T) {
	for _, err := range []error{
		nil,
		&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EADDRNOTAVAIL)},
		errors.New("something else"),
	} {
		if level, detail, _ := egressListenerStatus("127.0.0.1:7280", err, true, true); level == "FAIL" {
			t.Errorf("disabled proxy reported FAIL for err %v: %s", err, detail)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
