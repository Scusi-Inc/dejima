package capability

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod, not WriteFile's perm arg, so the umask can't strip the very bits
	// (e.g. group/world-write) a trust-gate test needs to set.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func adapter(dir string) *ScriptAdapter {
	return &ScriptAdapter{Dir: dir, Timeout: 3 * time.Second, MaxOutput: 1 << 16}
}

// Happy path: the script sees the args as JSON on stdin and the island identity
// as env; output is captured; exit 0.
func TestScriptAdapter_StdinAndEnv(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "greet", "#!/bin/sh\nprintf 'island=%s target=%s ' \"$DEJIMA_CAP_ISLAND\" \"$DEJIMA_CAP_TARGET\"\necho \"stdin=$(cat)\"\n", 0o700)

	res, err := adapter(dir).Execute(context.Background(), Request{
		Island: "oc", Agent: "o1", Target: "greet", Args: map[string]string{"name": "sam"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
	for _, want := range []string{"island=oc", "target=greet", `stdin={"name":"sam"}`} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output %q missing %q", res.Output, want)
		}
	}
}

// A target that runs and exits non-zero is a Result, not an adapter error.
func TestScriptAdapter_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "fail", "#!/bin/sh\necho boom\nexit 3\n", 0o700)
	res, err := adapter(dir).Execute(context.Background(), Request{Island: "oc", Target: "fail"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Output, "boom") {
		t.Errorf("output missing stdout: %q", res.Output)
	}
}

func TestScriptAdapter_NotFoundAndTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, target := range []string{"missing", "../etc/passwd", "a/b", "", ".."} {
		if _, err := adapter(dir).Execute(context.Background(), Request{Island: "oc", Target: target}); !errors.Is(err, ErrTargetNotFound) {
			t.Errorf("target %q: err = %v, want ErrTargetNotFound", target, err)
		}
	}
}

func TestScriptAdapter_TrustGates(t *testing.T) {
	dir := t.TempDir()
	// world-writable (still executable) → untrusted
	writeScript(t, dir, "ww", "#!/bin/sh\necho hi\n", 0o717)
	// not executable → untrusted
	writeScript(t, dir, "noexec", "#!/bin/sh\necho hi\n", 0o600)
	// symlink to a valid script → not a regular file → untrusted
	good := writeScript(t, dir, "good", "#!/bin/sh\necho hi\n", 0o700)
	if err := os.Symlink(good, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"ww", "noexec", "link"} {
		if _, err := adapter(dir).Execute(context.Background(), Request{Island: "oc", Target: target}); !errors.Is(err, ErrTargetUntrusted) {
			t.Errorf("target %q: err = %v, want ErrTargetUntrusted", target, err)
		}
	}
}

func TestScriptAdapter_Timeout(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "slow", "#!/bin/sh\nsleep 5\n", 0o700)
	a := &ScriptAdapter{Dir: dir, Timeout: 150 * time.Millisecond}
	start := time.Now()
	if _, err := a.Execute(context.Background(), Request{Island: "oc", Target: "slow"}); !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout did not kill the process promptly (took %s)", time.Since(start))
	}
}

func TestScriptAdapter_OutputCap(t *testing.T) {
	dir := t.TempDir()
	// emit far more than the cap on stdout
	writeScript(t, dir, "loud", "#!/bin/sh\nawk 'BEGIN{for(i=0;i<100000;i++)printf \"x\"}'\n", 0o700)
	a := &ScriptAdapter{Dir: dir, Timeout: 3 * time.Second, MaxOutput: 1000}
	res, err := a.Execute(context.Background(), Request{Island: "oc", Target: "loud"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if int64(len(res.Output)) > 1000 {
		t.Errorf("output len = %d, want <= 1000 (cap not enforced)", len(res.Output))
	}
}
