package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// The uninstall pre-flight refuses outright when it finds at-risk work, and the
// remedy it prints is "commit/push (or `dejima wake` to verify)". For a stopped
// repo-less island that instruction cannot be followed: there is no git and no
// remote, so waking it verifies nothing and there is nothing to push to.
//
// The gate itself is RIGHT to fire — an island with no remote is the one kind
// whose contents exist in exactly one place. What was wrong was the reason and
// the remedy, which sent the operator on an errand with no end.
func TestIslandAtRisk_StoppedNoRepoSaysWhatCanActuallyBeDone(t *testing.T) {
	isl := api.IslandInfo{Name: "brain", Container: "hibernated", NoRepo: true}

	// nil client: reaching the daemon would panic, so this proves the answer
	// comes from the record and not from a git probe that cannot exist here.
	reason := islandAtRisk(context.Background(), nil, isl)

	if reason == "" {
		t.Fatal("a stopped island with no remote must still block an uninstall — " +
			"it is the case where deletion is least recoverable")
	}
	if strings.Contains(reason, "unpushed") {
		t.Errorf("there is no remote, so nothing can be unpushed; got: %q", reason)
	}
	if !strings.Contains(reason, "no repo") {
		t.Errorf("the reason should name the actual cause; got: %q", reason)
	}
}

// The repo-backed message must not change — this is the common case and the
// wording carries the guard's whole meaning.
func TestIslandAtRisk_StoppedWithRepoUnchanged(t *testing.T) {
	isl := api.IslandInfo{Name: "work", Container: "hibernated"}
	reason := islandAtRisk(context.Background(), nil, isl)
	if !strings.Contains(reason, "unpushed work can't be verified") {
		t.Errorf("repo-backed wording regressed; got: %q", reason)
	}
}

// The field is useless if it doesn't survive the wire. A create-then-read
// round trip through the real daemon is the only thing that proves toInfo
// populates it — asserting the struct HAS the field proves nothing at all.
func TestIslandInfo_NoRepoSurvivesTheRoundTrip(t *testing.T) {
	_, c := cliEnv(t)

	if _, err := runCLI(t, "init", "--no-repo", "--name", "brain", "--agent", "claude-code"); err != nil {
		t.Fatalf("create: %v", err)
	}
	seedIsland(t, c, "work") // the repo-backed control

	got := map[string]bool{}
	islands, err := c.ListIslands(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, i := range islands {
		got[i.Name] = i.NoRepo
	}
	if !got["brain"] {
		t.Error("the repo-less island reports NoRepo=false — callers can't tell " +
			"'empty on purpose' from 'the clone failed', which is the entire point")
	}
	if got["work"] {
		t.Error("a repo-backed island reports NoRepo=true")
	}
}
