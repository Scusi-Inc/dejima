package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aoos/dejima/internal/project"
)

// An operator set a GH_TOKEN, was told to restart the agent, went looking for
// how, and ran `dejima reset`. Every Codex conversation in the island went with
// the home volume. Irreversibly.
//
// Every guard worked. reset warned, listed "all conversation history", and made
// them type the island name; `eject --include-home` would have saved it. All of
// that asks the operator to already know what they are about to lose and to act
// before they lose it — which is exactly the knowledge they do not have then.
//
// So the machine takes the copy.
func TestResetSnapshotsTheHomeVolumeFirst(t *testing.T) {
	h, f := twoAgentServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rr.Code, rr.Body.String())
	}
	p, err := project.Load("isl")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.HomeSnapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1 — the reset destroyed the volume with no copy", len(p.HomeSnapshots))
	}
	// ORDER IS THE WHOLE THING. A copy taken after the removal copies an empty
	// volume, restores cleanly, and gives back nothing — which is worse than no
	// snapshot, because it looks like protection.
	copied, removed := -1, -1
	for i, c := range f.volumeOps() {
		if c.op == "copy" && c.dst == p.HomeSnapshots[0].Volume {
			copied = i
		}
		if c.op == "remove" && c.name == "dejima-isl-home" && removed < 0 {
			removed = i
		}
	}
	if copied < 0 {
		t.Fatal("the home volume was never copied anywhere")
	}
	if removed < 0 {
		t.Fatal("the home volume was never removed — this test is not exercising a reset")
	}
	if copied > removed {
		t.Errorf("snapshot taken AFTER the volume was destroyed (copy at %d, remove at %d): "+
			"it would restore an empty home", copied, removed)
	}
}

// FAILS CLOSED. A snapshot skipped on error reads as protection right up until
// the one time it mattered, so a reset that cannot copy must not proceed.
func TestResetRefusesWhenItCannotSnapshot(t *testing.T) {
	h, f := twoAgentServer(t)
	f.copyVolumeErr = errors.New("no space left on device")

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")
	if rr.Code == http.StatusOK {
		t.Fatal("reset proceeded without a snapshot")
	}
	if !strings.Contains(rr.Body.String(), "Nothing was destroyed") {
		t.Errorf("the refusal does not say the island is intact, which is the "+
			"one thing the operator needs to know: %s", rr.Body.String())
	}
	// And it must actually be intact.
	for _, c := range f.volumeOps() {
		if c.op == "remove" && c.name == "dejima-isl-home" {
			t.Fatal("the home volume was removed after the snapshot failed")
		}
	}
}

// A failed copy must not leave an empty snapshot volume behind: it would restore
// cleanly, to nothing, which is the most expensive way to fail.
func TestAFailedSnapshotLeavesNoArtifact(t *testing.T) {
	h, f := twoAgentServer(t)
	f.copyVolumeErr = errors.New("copy blew up")
	do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")

	created, removed := map[string]bool{}, map[string]bool{}
	for _, c := range f.volumeOps() {
		if c.op == "ensure" && strings.Contains(c.name, "-home-snap-") {
			created[c.name] = true
		}
		if c.op == "remove" {
			removed[c.name] = true
		}
	}
	for v := range created {
		if !removed[v] {
			t.Errorf("empty snapshot volume %q left behind after a failed copy", v)
		}
	}
}

// Retention is bounded: these are full copies of a home volume, and an unbounded
// pile of them is its own outage.
func TestSnapshotsArePrunedOldestFirst(t *testing.T) {
	srv, _, _ := wakeServer(t)
	p := &project.Project{Name: "isl"}
	now := time.Now()
	for i := 0; i < homeSnapshotKeep+2; i++ {
		p.HomeSnapshots = append(p.HomeSnapshots, project.HomeSnapshot{
			Volume: "v" + string(rune('a'+i)), TakenAt: now.Add(time.Duration(i) * time.Minute),
		})
	}
	oldest := p.HomeSnapshots[0].Volume
	srv.pruneHomeSnapshots(context.Background(), p)

	if len(p.HomeSnapshots) != homeSnapshotKeep {
		t.Fatalf("kept %d, want %d", len(p.HomeSnapshots), homeSnapshotKeep)
	}
	for _, s := range p.HomeSnapshots {
		if s.Volume == oldest {
			t.Errorf("pruned the newest instead of the oldest: %q survived", oldest)
		}
	}
}

// Restore snapshots the CURRENT volume before overwriting it. Work done since
// the reset — a fresh login, a new conversation — is not the restore's to spend,
// and silently spending it would make this the same bug it exists to undo.
func TestRestoreSnapshotsBeforeOverwriting(t *testing.T) {
	h, _ := twoAgentServer(t)
	do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/restore", "{}")
	if rr.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rr.Code, rr.Body.String())
	}
	p, err := project.Load("isl")
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, s := range p.HomeSnapshots {
		reasons = append(reasons, s.Reason)
	}
	var sawRestore bool
	for _, r := range reasons {
		if r == "restore" {
			sawRestore = true
		}
	}
	if !sawRestore {
		t.Errorf("restore overwrote the home volume without copying it aside first; reasons=%v", reasons)
	}
}

// Asking for a snapshot that is not there must not silently restore a different
// one — the operator named a moment, and any other moment is the wrong answer.
func TestRestoreRefusesAnUnknownSnapshot(t *testing.T) {
	h, _ := twoAgentServer(t)
	do(t, h, http.MethodPost, "/v1/islands/isl/reset", "")

	rr := do(t, h, http.MethodPost, "/v1/islands/isl/restore", `{"volume":"dejima-isl-home-snap-1"}`)
	if rr.Code == http.StatusOK {
		t.Fatal("restored something other than what was asked for")
	}
}
