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
// Reading the baked value makes the two halves incapable of disagreeing: whatever
// the entrypoint is about to do, the non-primary agents do the same. `docker
// exec` inherits the container's configured environment, so no new runtime
// capability and no image change is needed — which matters, because an image fix
// would only reach islands upgraded after it shipped.
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
	stdout, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"printenv", "DEJIMA_LAUNCH"})
	if err != nil || code != 0 {
		return false
	}
	return strings.TrimSpace(stdout) == h.ResumeLaunch
}
