package localmodel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeServer writes an `ollama` that answers `list` only once a marker file
// exists — so "installed" and "running" are separately controllable, which is
// the distinction the whole file is about.
func fakeServer(t *testing.T) (exe, marker string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "ollama")
	marker = filepath.Join(dir, "up")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  list) [ -f " + marker + " ] && exit 0 || exit 1 ;;\n" +
		"  serve) touch " + marker + "; sleep 5 ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe, marker
}

// Start must wait for the server to ANSWER, not merely spawn it.
//
// The macOS install fallback backgrounded `ollama serve` with nohup and printed
// "starting the server directly". The operator's next screen said
//
//	status: installed (not running)
//
// "I started a process" and "the backend is up" are different claims, and
// reporting the first as the second is how an install finishes green while
// leaving nothing listening.
func TestStartWaitsForTheServerToAnswer(t *testing.T) {
	exe, marker := fakeServer(t)
	o := &Ollama{bin: exe}

	if _, running := o.Detect(context.Background()); running {
		t.Fatal("the fixture reports running before anything started it, so the " +
			"assertion below proves nothing")
	}
	if err := o.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the server was never actually launched: %v", err)
	}
	if _, running := o.Detect(context.Background()); !running {
		t.Error("Start returned success while the backend still does not answer")
	}
}

// A server that never answers must be an ERROR, not a silent success.
func TestStartFailsWhenTheServerNeverAnswers(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ollama")
	// Spawns fine, answers never.
	if err := os.WriteFile(exe, []byte("#!/bin/sh\ncase \"$1\" in serve) sleep 5 ;; esac\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevB, prevP := serverStartBudget, serverStartPoll
	serverStartBudget, serverStartPoll = 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { serverStartBudget, serverStartPoll = prevB, prevP })

	err := (&Ollama{bin: exe}).Start(context.Background())
	if err == nil {
		t.Fatal("a server that never answers reported success — the exact claim " +
			"that put `installed (not running)` on a screen saying the install finished")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("the error does not say what actually went wrong: %v", err)
	}
}

// Already running is a no-op, so an operator can run it twice and an install can
// call it unconditionally.
func TestStartIsANoOpWhenAlreadyRunning(t *testing.T) {
	exe, marker := fakeServer(t)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Ollama{bin: exe}).Start(context.Background()); err != nil {
		t.Fatalf("Start on an already-running backend must be a no-op: %v", err)
	}
}

// Nothing installed must say so, rather than reporting a start it did not do.
func TestStartWithNoBackendInstalledSaysSo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	orig := ollamaKnownPaths
	ollamaKnownPaths = []string{filepath.Join(t.TempDir(), "nope")}
	t.Cleanup(func() { ollamaKnownPaths = orig })

	err := (&Ollama{}).Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected a not-installed error, got %v", err)
	}
}

// The install script must not try to background a server itself.
//
// It did, with nohup, which only ignores SIGHUP — it does not leave the session,
// so the process dies with the shell that spawned it. The WSL launcher had
// already learned this and says so; the macOS fallback was written with nohup
// anyway. Starting is Start's job now, with setsid and a wait.
func TestTheInstallScriptDoesNotBackgroundAServer(t *testing.T) {
	script := darwinBrewScript("/opt/homebrew/bin/brew")
	if strings.Contains(script, "nohup") {
		t.Errorf("the install script backgrounds a server with nohup, which does not "+
			"survive the shell that started it:\n%s", script)
	}
	if !strings.Contains(script, "install ollama") {
		t.Errorf("the install script no longer installs anything:\n%s", script)
	}
}

// The server's output must go to a FILE, never to whatever stdio the daemon
// happens to have.
//
// Left nil, exec inherits ours — and a process we deliberately outlive then
// holds the daemon's stdout open indefinitely. It is not theoretical: the first
// version of this code hung `go test`, because the harness waits on the pipe
// rather than on the process. In production the same child would hold the
// daemon's log across a restart.
//
// Asserted by CONTENT, not by "did it hang": a hang is a property of whoever is
// reading the other end of the pipe, so a test that depends on it passes or
// fails for reasons that have nothing to do with this code. A mutation removing
// the redirection survived exactly that way.
func TestTheServerWritesToItsLogNotToOurStdio(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	exe := filepath.Join(dir, "ollama")
	marker := filepath.Join(dir, "up")
	const shout = "SERVER-STDOUT-MARKER"
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  list) [ -f " + marker + " ] && exit 0 || exit 1 ;;\n" +
		"  serve) echo " + shout + "; touch " + marker + "; sleep 2 ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&Ollama{bin: exe}).Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	logPath := filepath.Join(home, ".dejima", "ollama-server.log")
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no server log was written, so its output went somewhere we do not "+
			"control — most likely the daemon's own stdio: %v", err)
	}
	if !strings.Contains(string(b), shout) {
		t.Errorf("the server's stdout is not in %s, so it is going to inherited "+
			"stdio; got:\n%s", logPath, b)
	}
}
