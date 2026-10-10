package api

import (
	"context"
	"strings"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
)

// containerResumesPrimary reports whether this container's entrypoint will
// relaunch the PRIMARY agent with its resume command.
//
// It asks the container rather than the caller, and that is the whole point.
//
// Resume looked like a property of the CALL: upgrade passes resume=true, wake
// passes false. It is not. DEJIMA_LAUNCH is baked into the container env at
// CREATE time and image/start.sh re-reads it on EVERY start — a restart is a new
// PID 1 and a new tmux server, so the `if ! tmux has-session` branch always
// fires. An upgraded container therefore carries "claude --continue" for the
// rest of its life.
//
// So one hibernate/wake cycle after any upgrade — or a host reboot, or any
// docker restart — the primary resumed while reconcileAgents(resume=false) cold-
// started everyone else. Exactly the split the upgrade fix was written to
// prevent, reappearing one cycle later. Found by d4 reviewing that fix; the
// invariant it claimed ("this changes exactly one path") held at creation time
// and not for the container's lifetime.
//
// Reading what the entrypoint reads makes the two halves incapable of
// disagreeing: whatever it is about to do, the non-primary agents do the same.
// Since #333 that is the launch intent on images that support it (see
// launch_intent.go) and the baked value everywhere else — `docker exec` inherits
// the container's configured environment, so islands on an older image keep
// working with no recreate.
//
// Fails closed: any error, any unknown handler, any type with no resume
// affordance reports false, which is today's behaviour.
func (s *Server) containerResumesPrimary(ctx context.Context, p *project.Project) bool {
	pa := p.PrimaryAgent()
	if pa == nil {
		return false
	}
	h, ok := handlers.Lookup(pa.Type)
	if !ok || h.ResumeLaunch == "" {
		return false
	}
	return s.containerPrimaryLaunch(ctx, p) == h.ResumeLaunch
}

// containerPrimaryLaunch returns the command the entrypoint launches the primary
// with, mirroring image/start.sh's choice exactly: the launch intent when the
// image reads it and it is non-empty, otherwise the baked DEJIMA_LAUNCH. "" when
// the container cannot be asked.
//
// Read through the container, not from the host file, because the image decides:
// an older start.sh ignores the mount, and trusting the file there would report
// the daemon's intent while the entrypoint did something else.
func (s *Server) containerPrimaryLaunch(ctx context.Context, p *project.Project) string {
	name := p.ContainerName()
	if _, _, code, err := s.rt.Exec(ctx, name, []string{"test", "-e", launchIntentMarker}); err == nil && code == 0 {
		stdout, _, code, err := s.rt.Exec(ctx, name, []string{"cat", launchIntentMountPath + "/" + launchIntentFile})
		if err == nil && code == 0 && strings.TrimSpace(stdout) != "" {
			return strings.TrimSpace(stdout)
		}
	}
	stdout, _, code, err := s.rt.Exec(ctx, name, []string{"printenv", "DEJIMA_LAUNCH"})
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(stdout)
}
