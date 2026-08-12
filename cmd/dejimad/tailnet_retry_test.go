package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// serveTailnetWhenReady runs for the daemon's whole life when Tailscale never
// comes up. It has to exit on shutdown rather than pin a goroutine — and it must
// not wedge on the ticker.
func TestServeTailnetWhenReady_ExitsOnContextCancel(t *testing.T) {
	orig := tailnetRetryInterval
	tailnetRetryInterval = time.Millisecond
	defer func() { tailnetRetryInterval = orig }()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)

	done := make(chan struct{})
	go func() {
		// Port 0 would bind if we ever got that far; we shouldn't, because
		// `tailscale status` can't succeed in the test environment.
		serveTailnetWhenReady(ctx, log, "127.0.0.1:0", &http.Server{}, errCh)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond) // let it spin the loop a few times
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveTailnetWhenReady didn't return after ctx cancel — the daemon would leak it every shutdown")
	}

	select {
	case err := <-errCh:
		t.Fatalf("nothing should have been served, got %v", err)
	default:
	}
}

// The regression guard for the fresh-mini failure: dejimad was installed with
// --tcp :7273 before the operator had signed in to Tailscale, `tailscale status`
// exited non-zero, and the daemon returned that error — killing the unix socket
// along with it. launchd's KeepAlive then restarted it into a crash loop, which
// surfaced as "registered as a service but its socket never appeared". A
// tailnet that isn't up yet must never be fatal.
func TestTailscaleLookupIsNotFatal(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	lines := strings.Split(string(body), "\n")

	lookupRe := regexp.MustCompile(`loadTailscaleIPs\(log\)`)
	for i, line := range lines {
		if !lookupRe.MatchString(line) || strings.Contains(line, "func ") {
			continue
		}
		// Scan the error handling that follows this call for a bare `return`
		// of the error, which is what took the daemon down.
		end := i + 8
		if end > len(lines) {
			end = len(lines)
		}
		for j := i; j < end; j++ {
			l := strings.TrimSpace(lines[j])
			if strings.HasPrefix(l, "return fmt.Errorf(\"tailscale lookup") {
				t.Errorf("main.go:%d makes a Tailscale lookup failure fatal:\n\t%s\n"+
					"Tailscale not being up yet is transient — warn and keep serving the unix socket",
					j+1, l)
			}
		}
	}
}
