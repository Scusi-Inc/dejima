package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// The launch intent is how the daemon tells the entrypoint, per START, whether to
// relaunch the primary agent cold or resumed (#333).
//
// DEJIMA_LAUNCH alone cannot do it: it is baked into the container env at CREATE
// time, and `docker start` reuses the container. So whether a wake resumed was an
// accident of history — resumed if the island had ever been upgraded, cold if
// not — and no caller's choice could reach the primary.
//
// Instead the daemon writes the primary's launch command to a host file before
// each start it initiates, mounted read-only into the container; image/start.sh
// prefers it over $DEJIMA_LAUNCH. A start the daemon did NOT initiate (a crash
// under the restart policy, a host reboot's adopt) repeats the last intent.
const (
	launchIntentMountPath = "/opt/host/launch"
	launchIntentFile      = "primary"
	// launchIntentMarker exists only in images whose start.sh reads the intent.
	// An older image ignores the mount and goes on reading $DEJIMA_LAUNCH, so the
	// daemon must not trust the file there — version skew between daemon and
	// image is routine (`dejima image` lags `dejima update`).
	launchIntentMarker = "/opt/dejima/features/launch-intent"
)

// primaryLaunch is the command the entrypoint should run for the primary agent,
// cold or resumed. Empty for headless and agentless islands, and for unknown
// types — start.sh treats an empty intent as absent and falls back.
func primaryLaunch(p *project.Project, resume bool) string {
	pa := p.PrimaryAgent()
	if pa == nil {
		return ""
	}
	h, ok := handlers.Lookup(pa.Type)
	if !ok {
		return ""
	}
	return h.LaunchFor(resume)
}

// writeLaunchIntent records the primary's launch for the container's next start.
// Atomic (temp file + rename in the same dir) so a start racing the write reads
// the old command or the new one, never half of one.
func writeLaunchIntent(p *project.Project, resume bool) error {
	dir, err := paths.LaunchIntentDir(p.Name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".primary-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.WriteString(primaryLaunch(p, resume)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, launchIntentFile))
}

// launchIntentBind is the read-only mount that carries the intent into the
// container. Only containers created after it existed have it; older ones fall
// back to their baked DEJIMA_LAUNCH, which is the behaviour before #333.
func launchIntentBind(p *project.Project) (runtime.BindMount, error) {
	dir, err := paths.LaunchIntentDir(p.Name)
	if err != nil {
		return runtime.BindMount{}, err
	}
	return runtime.BindMount{HostPath: dir, ContainerPath: launchIntentMountPath, ReadOnly: true}, nil
}

// startWithIntent starts an existing, stopped container, first recording whether
// its primary should resume. Every path that starts a container on purpose goes
// through here so the choice is the caller's, not the container's history.
//
// A failed write is logged, not fatal: the container still starts, and
// containerResumesPrimary reads back whatever the entrypoint will actually do, so
// the agents stay consistent with each other either way.
func (s *Server) startWithIntent(ctx context.Context, p *project.Project, resume bool) error {
	if err := writeLaunchIntent(p, resume); err != nil {
		s.log.Warn("launch intent: write failed; the container's baked launch decides", "island", p.Name, "err", err)
	}
	if err := s.rt.StartContainer(ctx, p.ContainerName()); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}
