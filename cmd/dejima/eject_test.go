package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// TestVolumeNames pins the eject command to the SAME deterministic volume names
// the daemon uses (project.WorkspaceVolume/HomeVolume). If these ever drift,
// eject would copy the wrong (or no) volume.
func TestVolumeNames(t *testing.T) {
	ws, home := volumeNames("myrepo")
	if ws != "dejima-myrepo-workspace" {
		t.Errorf("workspace volume = %q, want dejima-myrepo-workspace", ws)
	}
	if home != "dejima-myrepo-home" {
		t.Errorf("home volume = %q, want dejima-myrepo-home", home)
	}
}

// TestDirIsPopulated is the clobber guard's core: missing and empty dirs are
// safe eject targets, a dir with any entry is the refuse-without-force case.
func TestDirIsPopulated(t *testing.T) {
	base := t.TempDir()

	// Missing directory → not populated, no error.
	missing := filepath.Join(base, "does-not-exist")
	if pop, err := dirIsPopulated(missing); err != nil || pop {
		t.Errorf("missing dir: got (pop=%v, err=%v), want (false, nil)", pop, err)
	}

	// Empty directory → not populated.
	empty := filepath.Join(base, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if pop, err := dirIsPopulated(empty); err != nil || pop {
		t.Errorf("empty dir: got (pop=%v, err=%v), want (false, nil)", pop, err)
	}

	// Non-empty directory → populated (the clobber case).
	full := filepath.Join(base, "full")
	if err := os.Mkdir(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if pop, err := dirIsPopulated(full); err != nil || !pop {
		t.Errorf("non-empty dir: got (pop=%v, err=%v), want (true, nil)", pop, err)
	}
}

// TestEjectIdleWarning checks each non-idle condition surfaces a warning and a
// fully-idle, clean, pushed island produces none (so eject stays quiet then).
func TestEjectIdleWarning(t *testing.T) {
	cases := []struct {
		name string
		isl  *api.IslandInfo
		want []string // substrings that must appear; empty means no warning
	}{
		{
			name: "nil island",
			isl:  nil,
		},
		{
			name: "idle, clean, pushed → no warning",
			isl:  &api.IslandInfo{Container: "stopped", Git: &api.GitInfo{Clean: true}},
		},
		{
			name: "running container warns about snapshot",
			isl:  &api.IslandInfo{Container: "running", Git: &api.GitInfo{Clean: true}},
			want: []string{"running", "snapshot"},
		},
		{
			name: "uncommitted changes warn",
			isl:  &api.IslandInfo{Container: "stopped", Git: &api.GitInfo{Clean: false, DirtyFiles: 3}},
			want: []string{"3 uncommitted changes"},
		},
		{
			name: "unpushed commits warn",
			isl:  &api.IslandInfo{Container: "stopped", Git: &api.GitInfo{Clean: true, Ahead: 2}},
			want: []string{"2 unpushed commits", "extracted .git"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ejectIdleWarning(tc.isl)
			if len(tc.want) == 0 {
				if got != "" {
					t.Errorf("got warning %q, want none", got)
				}
				return
			}
			for _, sub := range tc.want {
				if !strings.Contains(got, sub) {
					t.Errorf("warning %q missing %q", got, sub)
				}
			}
		})
	}
}

// TestEjectCmdShape locks the command's contract: name, two-arg requirement, and
// the three flags (the clobber/scope/confirm knobs) so a refactor can't quietly
// drop them.
func TestEjectCmdShape(t *testing.T) {
	cmd := newEjectCmd()
	if !strings.HasPrefix(cmd.Use, "eject ") {
		t.Errorf("Use = %q, want it to start with 'eject '", cmd.Use)
	}
	// The command must be wired into the root tree under the name "eject" (this
	// also satisfies the coverage gate, which credits the quoted-token form).
	if _, _, err := newRootCmd().Find([]string{"eject"}); err != nil {
		t.Errorf("`dejima eject` not registered on the root command: %v", err)
	}
	if err := cmd.Args(cmd, []string{"only-one"}); err == nil {
		t.Error("expected an error for a single arg (eject needs <island> <dest-dir>)")
	}
	if err := cmd.Args(cmd, []string{"island", "dest"}); err != nil {
		t.Errorf("two args should be accepted, got %v", err)
	}
	for _, f := range []string{"include-home", "force", "yes"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("missing --%s flag", f)
		}
	}
}
