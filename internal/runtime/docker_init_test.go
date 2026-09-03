package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubDocker writes a fake `docker` that records its argv and prints a container
// id, and returns the path to the recording.
//
// The argv is asserted through the REAL CreateContainer rather than by restating
// the flag list, because the bug this guards is a missing element in a slice that
// nothing else reads. A test that rebuilt the expected args would pass with the
// production line unchanged in either direction.
func stubDocker(t *testing.T) (*Docker, string) {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + rec + "\necho deadbeef\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Docker{Bin: bin}, rec
}

// Islands run `tail -f /dev/null` as PID 1, which never calls wait(). Without
// tini in front of it, every grandchild whose parent exits first is reparented
// there and becomes a zombie permanently — a zombie cannot be killed, only
// reaped. Measured in a 29-hour-old island: 541 zombies of 572 processes.
func TestCreateContainerReapsOrphans(t *testing.T) {
	d, rec := stubDocker(t)
	if _, err := d.CreateContainer(context.Background(), CreateRequest{
		Name:  "isl",
		Image: "dejima/island:latest",
	}); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("the stub docker was never invoked: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(b)), "\n")

	var sawInit bool
	for _, a := range argv {
		if a == "--init" {
			sawInit = true
		}
	}
	if !sawInit {
		t.Errorf("`docker run` has no --init, so nothing reaps orphaned processes in "+
			"the island and they accumulate as zombies for the container's lifetime.\nargv: %v", argv)
	}
	// The control: if the stub ever stops capturing, the check above goes hollow
	// and keeps passing. Assert we actually read a plausible run command.
	if len(argv) == 0 || argv[0] != "run" {
		t.Fatalf("the recording is not a docker run invocation — this guard is not "+
			"reading what it thinks it is.\nargv: %v", argv)
	}
}
