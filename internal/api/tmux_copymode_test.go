package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// `mouse on` — which image/tmux.conf sets, and tmux defaults to OFF — makes a
// wheel-scroll enter COPY-MODE, and tmux keeps the arrow keys for itself while
// there. So the most ordinary action available (scroll up to read a long
// transcript, scroll back down) silently half-disabled the operator's keyboard:
// arrows stopped reaching the agent's UI and nothing on screen said why.
//
// It surfaced as "I'm unable to move through the options vertically. Is dejima
// causing the problem?" on a Claude Code picker. It was.
//
// The trap is self-concealing, which is why it needs a test rather than a note:
// Esc exits copy-mode, Esc is what anyone presses when a UI stops responding,
// so the usual recovery works without ever revealing the cause — and the trap
// re-arms on the next scroll.
//
// Asserted against the REAL tmux against the REAL config file, because the
// subject is an interaction between two settings and a binding. A test that
// grepped the config for the binding would pass with the binding misspelled.
func tmuxLab(t *testing.T) (socket string, run func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	conf, err := filepath.Abs(filepath.Join("..", "..", "image", "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(conf); err != nil {
		t.Fatalf("image/tmux.conf: %v", err)
	}
	socket = "dejimatest" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
	run = func(args ...string) string {
		out, _ := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	// A pane with enough output to have a scrollback worth scrolling.
	if out, err := exec.Command("tmux", "-f", conf, "-L", socket, "new-session", "-d",
		"-s", "k", "-x", "80", "-y", "8",
		"sh -c 'i=1; while [ $i -le 60 ]; do echo line-$i; i=$((i+1)); done; sleep 30'",
	).CombinedOutput(); err != nil {
		t.Skipf("tmux would not start here: %v: %s", err, out)
	}
	time.Sleep(1500 * time.Millisecond)
	return socket, run
}

func inMode(run func(...string) string) string {
	return run("display-message", "-p", "-t", "k", "#{pane_in_mode}")
}

// Scrolling back to the bottom must LEAVE copy-mode, so the pane heals itself
// the moment the operator does the natural thing.
func TestScrollingBackToTheBottomExitsCopyMode(t *testing.T) {
	_, run := tmuxLab(t)

	run("copy-mode", "-t", "k")
	if got := inMode(run); got != "1" {
		t.Fatalf("pane_in_mode = %q after copy-mode — this test is not exercising copy-mode", got)
	}
	run("send-keys", "-t", "k", "WheelDownPane")
	time.Sleep(400 * time.Millisecond)

	if got := inMode(run); got != "0" {
		t.Errorf("still in copy-mode after scrolling back to the bottom (%q): "+
			"the arrow keys stay captured and the agent's UI stops responding", got)
	}
}

// ...but NOT early. A wheel-down with scrollback still above must scroll, not
// jump the operator out of the history they are reading. The fix must not cost
// the feature.
func TestScrollingUpStillWorks(t *testing.T) {
	_, run := tmuxLab(t)

	run("copy-mode", "-t", "k")
	for i := 0; i < 3; i++ {
		run("send-keys", "-t", "k", "-X", "scroll-up")
	}
	time.Sleep(300 * time.Millisecond)
	pos := run("display-message", "-p", "-t", "k", "#{scroll_position}")
	if pos == "0" || pos == "" {
		t.Skipf("could not scroll this pane (position %q) — nothing to assert", pos)
	}

	run("send-keys", "-t", "k", "WheelDownPane")
	time.Sleep(300 * time.Millisecond)
	if got := inMode(run); got != "1" {
		t.Errorf("left copy-mode with %s lines still scrolled back — a wheel-down "+
			"in the middle of the history must scroll, not exit", pos)
	}
}

// The setting that creates the hazard. If mouse is ever turned off, the bindings
// above are dead weight and this whole family stops applying — worth failing
// loudly rather than leaving two tests that pass by not applying.
func TestMouseIsOnWhichIsWhyTheBindingsExist(t *testing.T) {
	_, run := tmuxLab(t)
	if got := run("show", "-g", "mouse"); !strings.Contains(got, "on") {
		t.Fatalf("mouse is %q — tmux defaults it OFF, and these copy-mode bindings "+
			"exist only because this config turns it on", got)
	}
}
