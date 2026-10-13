package api

import (
	"context"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime/runtimetest"
)

// Purge must remove the home SNAPSHOT volumes, not just the home volume.
//
// Each snapshot is a full copy of an island's home volume (snapshotHome), kept
// homeSnapshotKeep deep and taken automatically before every destructive
// operation — so an island that was reset a few times carries several GB of
// them. teardown ends in project.Delete, which drops the only record naming
// those volumes, so a snapshot left behind here is unreachable forever: no
// dejima surface lists it, and no later purge can find it to retry. The disk it
// holds is only visible to `docker volume ls`, which is where this was found —
// an operator out of space on a host whose islands had all been purged.
//
// Asserted through the fake's RemovedVolumes rather than a count: the bug was
// removing the right NUMBER of volumes (container, workspace, home) while
// skipping the snapshots entirely.
func TestTeardown_RemovesHomeSnapshotVolumes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		HomeSnapshots: []project.HomeSnapshot{
			{Volume: "dejima-isl-home-snap-1700000000", TakenAt: time.Now().UTC(), Reason: "reset"},
			{Volume: "dejima-isl-home-snap-1700000100", TakenAt: time.Now().UTC(), Reason: "reset"},
			{Volume: "dejima-isl-home-snap-1700000200", TakenAt: time.Now().UTC(), Reason: "restore"},
		},
	}
	if err := p.Save(); err != nil {
		t.Fatalf("save project: %v", err)
	}

	fake := &runtimetest.Fake{}
	s := &Server{rt: fake}
	if err := s.teardown(context.Background(), p, true); err != nil {
		t.Fatalf("teardown: %v", err)
	}

	removed := map[string]bool{}
	for _, v := range fake.RemovedVolumes() {
		removed[v] = true
	}
	// The two that always worked, so a regression here is distinguishable from
	// the snapshot bug rather than showing up as the same failure.
	for _, want := range []string{p.WorkspaceVolume(), p.HomeVolume()} {
		if !removed[want] {
			t.Errorf("teardown did not remove %q; removed %v", want, fake.RemovedVolumes())
		}
	}
	for _, snap := range p.HomeSnapshots {
		if !removed[snap.Volume] {
			t.Errorf("teardown left snapshot volume %q behind — purge orphans it, "+
				"and project.Delete has just dropped the record naming it; removed %v",
				snap.Volume, fake.RemovedVolumes())
		}
	}
}

// An island with no snapshots must still tear down cleanly — the loop is the
// new code, and "works only when the list is non-empty" is the shape of bug a
// single fixture would hide.
func TestTeardown_NoSnapshotsIsClean(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	p := &project.Project{Name: "bare", DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatalf("save project: %v", err)
	}

	fake := &runtimetest.Fake{}
	s := &Server{rt: fake}
	if err := s.teardown(context.Background(), p, true); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if got := len(fake.RemovedVolumes()); got != 2 {
		t.Errorf("removed %d volumes, want exactly workspace+home: %v", got, fake.RemovedVolumes())
	}
}
