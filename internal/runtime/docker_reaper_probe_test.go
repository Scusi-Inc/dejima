package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CreateContainer passing --init makes new islands reap. It says nothing about
// the ones already running: --init is fixed at create time, so every container
// made before that flag keeps leaking zombies for its whole life while the
// daemon's source insists otherwise. ContainerReapsOrphans is how a surface
// finds that out, and it can only find out by asking the engine.
//
// These tests exist because the query itself was unguarded: swapping
// {{.HostConfig.Init}} for another field left the whole suite green.

// stubDockerOutput writes a fake `docker` that records argv and prints out.
func stubDockerOutput(t *testing.T, out string) (*Docker, string) {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "docker")
	// out is single-quoted: docker renders an unset Init as "<no value>", and an
	// unquoted `echo <no value>` is a shell redirect, not output. The first run of
	// this test failed exactly that way.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + rec + "\nprintf '%s\\n' '" + out + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Docker{Bin: bin}, rec
}

// The field name is the entire mechanism. Asserting the boolean it returns would
// pass just as well against .HostConfig.AutoRemove — verified: that mutation
// survived the whole suite before this test existed.
func TestContainerReapsOrphansAsksTheInitField(t *testing.T) {
	d, rec := stubDockerOutput(t, "true")
	if _, err := d.ContainerReapsOrphans(context.Background(), "isl"); err != nil {
		t.Fatalf("ContainerReapsOrphans: %v", err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("the stub docker was never invoked: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(b)), "\n")
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "{{.HostConfig.Init}}") {
		t.Errorf("the probe doesn't read .HostConfig.Init, so it is reporting something "+
			"other than whether this container reaps.\nargv: %v", argv)
	}
	// Control: if the stub stops recording, the assertion above goes hollow and
	// keeps passing. Require the recording to be a real inspect.
	if len(argv) == 0 || argv[0] != "inspect" {
		t.Fatalf("the recording is not a docker inspect — this guard is not reading "+
			"what it thinks it is.\nargv: %v", argv)
	}
}

// Docker renders an unset Init as "<no value>", not "false". Both mean no
// reaper. Treating "<no value>" as anything other than false would report every
// pre---init container as reaping — the exact reassurance this is meant to deny.
func TestContainerReapsOrphansReadsDockersBooleans(t *testing.T) {
	for out, want := range map[string]bool{
		"true":       true,
		"false":      false,
		"<no value>": false,
	} {
		d, _ := stubDockerOutput(t, out)
		got, err := d.ContainerReapsOrphans(context.Background(), "isl")
		if err != nil {
			t.Fatalf("output %q: %v", out, err)
		}
		if got != want {
			t.Errorf("docker printed %q → %v, want %v", out, got, want)
		}
	}
}

// "no reaper" and "couldn't look" must not share a return value. A caller uses
// this to decide whether to warn an operator, and a failed inspect rendered as
// false accuses a container nobody inspected.
func TestContainerReapsOrphansErrorsRatherThanGuessing(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'no such container' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Docker{Bin: bin}
	if _, err := d.ContainerReapsOrphans(context.Background(), "gone"); err == nil {
		t.Error("a failed inspect must be an error, not a confident false")
	}
}
