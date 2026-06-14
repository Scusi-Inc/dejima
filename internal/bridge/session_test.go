package bridge

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseMaxClientSize(t *testing.T) {
	cases := []struct {
		in     string
		h, w   uint16
		wantOK bool
	}{
		{"24 80", 24, 80, true},
		{"24 80\n50 120\n40 100", 50, 120, true}, // max per axis, independently
		{"", 0, 0, false},
		{"garbage", 0, 0, false},
		{"24 x", 0, 0, false},
		{"  30 90  ", 30, 90, true}, // trimmed
	}
	for _, c := range cases {
		h, w, ok := parseMaxClientSize(c.in)
		if h != c.h || w != c.w || ok != c.wantOK {
			t.Errorf("parseMaxClientSize(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, h, w, ok, c.h, c.w, c.wantOK)
		}
	}
}

// TestHostPTYRunsCommand smoke-tests the host (no-container) PTY path: a command
// runs directly on this host and its output comes back through the PTY.
func TestHostPTYRunsCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := HostPTY(ctx, []string{"echo", "dejima-host-ok"}, 24, 80)
	if err != nil {
		t.Skipf("no PTY available in this environment: %v", err)
	}
	defer s.Close()

	var got strings.Builder
	buf := make([]byte, 256)
	for {
		n, rerr := s.Read(buf)
		if n > 0 {
			got.WriteString(string(buf[:n]))
		}
		if rerr != nil {
			break
		}
		if strings.Contains(got.String(), "dejima-host-ok") {
			break
		}
	}
	if !strings.Contains(got.String(), "dejima-host-ok") {
		t.Errorf("HostPTY output = %q, want it to contain the echoed marker", got.String())
	}
}

func TestHostPTYEmptyCommand(t *testing.T) {
	if _, err := HostPTY(context.Background(), nil, 0, 0); err == nil {
		t.Error("HostPTY(nil) should error")
	}
}
