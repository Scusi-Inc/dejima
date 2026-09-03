package main

import (
	"context"
	"strings"
	"testing"
)

// `dejima home create` used to hard-refuse a brain with no repo:
//
//	--repo is required (the brain's config/workspace repo); repo-less home
//	islands are a follow-up
//
// which is backwards for the case Home Islands exist to serve. An openclaw-style
// brain self-installs and keeps its state in the island; requiring a git repo
// meant inventing an empty one purely to satisfy a flag.
//
// These tests drive the real cobra tree against the in-proc daemon (cliEnv), so
// they assert what the command DOES, not that the code is present. A test that
// only proves an error appears would pass just as happily against a command that
// rejects everything.

// The guards. Each of these must fail, and fail with the reason that tells the
// caller what to do instead.
func TestHomeCreate_NoRepoGuards(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			// The whole reason --no-repo is an explicit flag rather than "--repo
			// may be empty": a URL the shell ate is indistinguishable from intent,
			// and the difference only surfaces later as a brain missing its config.
			name: "bare create still demands a repo",
			args: []string{"home", "create", "--agent", "openclaw"},
			want: "--no-repo",
		},
		{
			// Nothing to derive a name FROM. Generating one would produce brains
			// nobody can predict the name of.
			name: "no-repo requires a name",
			args: []string{"home", "create", "--agent", "openclaw", "--no-repo"},
			want: "--name is required",
		},
		{
			// Guessing which one was meant is how you clone into an island someone
			// believed was empty.
			name: "no-repo conflicts with repo",
			args: []string{"home", "create", "--agent", "openclaw", "--no-repo",
				"--name", "brain", "--repo", "https://github.com/a/b"},
			want: "pick one",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cliEnv(t)
			_, err := runCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("expected a rejection, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// The positive control, and the one that actually proves the feature. Without
// this the guards above would pass against a `home create` that refuses every
// input — the failure mode this command was already in.
//
// It runs the create all the way through the daemon and then reads the island
// back, so "it worked" means an island exists, not that a validation switch
// fell through.
func TestHomeCreate_NoRepoCreatesAnEmptyBrain(t *testing.T) {
	_, c := cliEnv(t)

	out, err := runCLI(t, "home", "create", "--agent", "openclaw", "--no-repo", "--name", "brain")
	if err != nil {
		t.Fatalf("repo-less home create should succeed, got: %v\noutput:\n%s", err, out)
	}

	// The note is the user-visible signal that an empty workspace was DELIBERATE.
	// A silent empty /workspace is exactly what a failed clone looks like.
	if !strings.Contains(out, "no repo") {
		t.Errorf("output should say the workspace starts empty on purpose, got:\n%s", out)
	}

	info, err := c.GetIsland(context.Background(), "brain")
	if err != nil {
		t.Fatalf("the island should exist after a successful create: %v", err)
	}
	if info.Name != "brain" {
		t.Errorf("island name = %q, want %q", info.Name, "brain")
	}
}

// A repo-less create must not quietly become a repo-backed one. This is the
// assertion that would catch a future refactor threading a default repo through
// the home path — the failure would otherwise be invisible until a brain
// mysteriously had someone else's code in it.
func TestHomeCreate_NoRepoRecordsNoOrigin(t *testing.T) {
	_, c := cliEnv(t)

	if _, err := runCLI(t, "home", "create", "--agent", "openclaw", "--no-repo", "--name", "brain"); err != nil {
		t.Fatalf("create: %v", err)
	}
	islands, err := c.ListIslands(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, i := range islands {
		if i.Name != "brain" {
			continue
		}
		found = true
		if i.Repo != "" {
			t.Errorf("a repo-less island recorded a repo (%q) — it has no origin to record", i.Repo)
		}
	}
	if !found {
		t.Fatal("created island missing from the list")
	}
}
