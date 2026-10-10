package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// homeSnapMarker is the infix snapshotHome puts in a snapshot volume's name:
// "dejima-<island>-home-snap-<unix>". Repeated here rather than imported
// because doctor is a client and must not depend on the daemon's internals —
// the same reason defaultEgressProxyPort is repeated in doctor_egress.go.
const homeSnapMarker = "-home-snap-"

// dockerVolumeCommand is the test seam for the docker shell-out, matching
// netstatCommand in doctor_egress.go.
var dockerVolumeCommand = exec.Command

// checkOrphanedSnapshots reports home-snapshot volumes whose island is gone.
//
// Purge used to remove an island's container, both volumes and network but not
// its home SNAPSHOTS — full copies of the home volume, kept three deep. Because
// purge also deletes the project record that names them, every snapshot left
// behind became unreachable: nothing listed it, and no later purge could find
// it. Hosts ran out of disk with no dejima surface showing why.
//
// The leak is fixed, but the orphans already on disk are not; nothing else
// looks for them, so this check is how they become visible. It needs no daemon
// and no island list: an island that still exists always has its home volume
// (hibernate preserves it), so a snapshot whose "<island>-home" is missing is
// orphaned by definition.
func checkOrphanedSnapshots(ctx context.Context, r *doctorReport) {
	if where, remote := daemonElsewhere(); remote {
		// Reported, not skipped: a host where the check never ran must not be
		// indistinguishable from one with nothing to reclaim.
		r.add("System", "snapshot volumes", "INFO",
			"not measured — Docker runs on the daemon host ("+where+"), not here",
			"run `dejima doctor` on "+where+" to see reclaimable snapshot volumes")
		return
	}
	out, err := dockerVolumeCommand("docker", "volume", "ls", "--format", "{{.Name}}").Output()
	if err != nil {
		r.add("System", "snapshot volumes", "INFO",
			fmt.Sprintf("not measured — could not list docker volumes: %v", err),
			"this is advisory; nothing else is blocked by it")
		return
	}
	orphans, live := classifySnapshotVolumes(strings.Split(string(out), "\n"))
	detail := fmt.Sprintf("%d snapshot volume(s); %d belong to islands that still exist", live+len(orphans), live)
	if len(orphans) == 0 {
		r.add("System", "snapshot volumes", "OK", detail, "")
		return
	}
	// Name them. The operator cannot discover these any other way — that is the
	// whole reason this check exists — so a warning that only counts them would
	// leave them exactly as unreachable as before.
	r.add("System", "snapshot volumes", "WARN",
		fmt.Sprintf("%s — %d are orphaned by a purged island and nothing will ever reclaim them: %s",
			detail, len(orphans), strings.Join(orphans, " ")),
		"each is a full copy of a deleted island's home volume; remove with:\n    docker volume rm "+
			strings.Join(orphans, " ")+"\n  (sizes: `docker system df -v`)")
}

// classifySnapshotVolumes splits snapshot volume names into those whose island
// is gone and a count of those still backing a live island. Pure, so the
// decision table is testable without Docker.
func classifySnapshotVolumes(names []string) (orphans []string, live int) {
	have := map[string]bool{}
	var snaps []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		have[n] = true
		if strings.Contains(n, homeSnapMarker) {
			snaps = append(snaps, n)
		}
	}
	for _, s := range snaps {
		island, ok := islandFromSnapshotVolume(s)
		if !ok {
			continue
		}
		if have["dejima-"+island+"-home"] {
			live++
			continue
		}
		orphans = append(orphans, s)
	}
	return orphans, live
}

// islandFromSnapshotVolume recovers the island name from a snapshot volume.
//
// LastIndex, not Index: an island may legitimately be named something ending in
// "-home" (docs/runbook-openclaw-home-island.md ships one called oc-home), whose
// snapshots are "dejima-oc-home-home-snap-<unix>". Splitting on the FIRST marker
// yields island "oc", whose home volume does not exist — so every snapshot of a
// live island would be reported as reclaimable, and the remedy this check prints
// would delete the backups of an island the operator still has.
func islandFromSnapshotVolume(v string) (string, bool) {
	rest, ok := strings.CutPrefix(v, "dejima-")
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, homeSnapMarker)
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}
