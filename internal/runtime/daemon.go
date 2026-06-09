package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	goruntime "runtime"
	"time"
)

// DaemonReachable reports whether the container runtime's server is up and
// answering. It shells out to `docker version` and checks for a server build.
func (d *Docker) DaemonReachable(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// `docker version --format {{.Server.Version}}` exits non-zero when the
	// CLI is present but the daemon isn't reachable — exactly the signal we want.
	return exec.CommandContext(cctx, d.bin(), "version", "--format", "{{.Server.Version}}").Run() == nil
}

// EnsureDaemon makes the container runtime ready: if `docker version` already
// reaches a server it returns immediately, otherwise it starts a local runtime
// (colima / Docker Desktop / OrbStack / systemd docker) when one is installed
// and polls until the daemon answers. This is what lets dejimad be the single
// thing a host operator starts — the VM/runtime becomes dejimad's private
// dependency rather than something the user babysits.
//
// Best-effort by contract: the caller logs and continues on error so the API
// (doctor, status) still serves and the runtime can come up later.
func (d *Docker) EnsureDaemon(ctx context.Context, log *slog.Logger) error {
	if d.DaemonReachable(ctx) {
		return nil
	}
	starter := detectRuntimeStarter()
	if starter == nil {
		return errors.New("container runtime not reachable and no known runtime " +
			"(colima / Docker Desktop / OrbStack) found to start")
	}

	log.Info("container runtime not running; starting it", "runtime", starter.name)
	if err := starter.start(ctx); err != nil {
		return fmt.Errorf("start %s: %w", starter.name, err)
	}

	// A cold VM boot (colima / Docker Desktop) can take ~30–60s. Poll until the
	// daemon answers or we give up.
	deadline := time.Now().Add(90 * time.Second)
	for {
		if d.DaemonReachable(ctx) {
			log.Info("container runtime is ready", "runtime", starter.name)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s started but the docker daemon is still unreachable after 90s", starter.name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// runtimeStarter names a local container runtime and knows how to start it.
type runtimeStarter struct {
	name  string
	start func(ctx context.Context) error
}

// runShell starts a command, capturing stderr so a failure carries a useful message.
func runShell(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := stderr.String(); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// detectRuntimeStarter picks the first installed runtime we know how to start.
// colima wins when present: it's CLI-managed and works headless (the common
// case on a Mac mini host reached over SSH). The order otherwise mirrors
// scripts/setup.sh so the daemon and the installer agree.
func detectRuntimeStarter() *runtimeStarter {
	if path, err := exec.LookPath("colima"); err == nil {
		return &runtimeStarter{name: "colima", start: func(ctx context.Context) error {
			return runShell(ctx, path, "start")
		}}
	}

	switch goruntime.GOOS {
	case "darwin":
		// OrbStack ships an `orb` CLI; fall back to launching the .app.
		if path, err := exec.LookPath("orb"); err == nil {
			return &runtimeStarter{name: "orbstack", start: func(ctx context.Context) error {
				return runShell(ctx, path, "start")
			}}
		}
		if _, err := os.Stat("/Applications/OrbStack.app"); err == nil {
			return &runtimeStarter{name: "orbstack", start: func(ctx context.Context) error {
				return runShell(ctx, "open", "-ga", "OrbStack")
			}}
		}
		if _, err := os.Stat("/Applications/Docker.app"); err == nil {
			return &runtimeStarter{name: "docker-desktop", start: func(ctx context.Context) error {
				return runShell(ctx, "open", "-ga", "Docker")
			}}
		}
	case "linux":
		// systemd-managed docker engine — the standard Linux host setup.
		if _, err := exec.LookPath("systemctl"); err == nil {
			return &runtimeStarter{name: "docker (systemd)", start: func(ctx context.Context) error {
				return runShell(ctx, "systemctl", "start", "docker")
			}}
		}
	}
	return nil
}
