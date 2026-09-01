package main

import "fmt"

// dockerUnreachableError explains a daemon-side Docker outage to someone who is
// almost certainly looking at a different machine.
//
// The failure it replaces: island creation ran a full `docker build` that could
// not possibly succeed, then reported "docker build failed: exit status 1"
// followed by docker's own "Cannot connect to the Docker daemon at
// unix:///Users/<someone>/.docker/run/docker.sock". Every word of that is true
// and it points at the wrong computer. The person reading it is on a laptop;
// the socket is on the host. One operator reasonably tried sudo on the laptop,
// which cannot affect the host and quietly makes things worse — as root the CLI
// reads root's config, not theirs, so it may not even find the profile.
//
// The daemon already reports DockerReachable in the overview the creator
// fetches immediately before building. It was there the whole time and only
// IslandImagePresent was read from it.
//
// Docker Desktop on macOS is the common case and the reason this recurs: it is
// a GUI application whose socket lives in the logged-in user's home, so a Mac
// mini that rebooted with nobody signed in has no Docker at all — and headless
// Macs are exactly what people use as a Dejima host.
func dockerUnreachableError(host string) error {
	where := "the Dejima host"
	if host != "" {
		where = host
	}
	return fmt.Errorf(`Docker isn't running on %s.

Islands are built and run on that machine, not on this one — so starting
Docker here won't help, and neither will sudo (that changes which config
this CLI reads, not which machine does the work).

On %s:
  • Make sure Docker Desktop is running. It's a desktop app: if that Mac
    rebooted and nobody signed in, Docker never started.
  • Then this will succeed there:  docker info
  • So a reboot recovers on its own, turn on Docker Desktop → Settings →
    General → "Start Docker Desktop when you sign in", and set that Mac to
    log in automatically.

Nothing was created, and no island was harmed — this stopped before the
build started.`, where, where)
}
