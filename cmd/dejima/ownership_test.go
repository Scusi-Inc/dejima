package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func TestGroupByRepo(t *testing.T) {
	items := []api.IslandInfo{
		{Name: "web-1", Repo: "git@h/web"},
		{Name: "api-1", Repo: "git@h/api"},
		{Name: "web-2", Repo: "git@h/web"},
		{Name: "scratch"}, // no repo
	}
	groups := groupByRepo(items)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}
	// First-seen repo order: web, api, (no repo).
	if groups[0].repo != "git@h/web" || len(groups[0].islands) != 2 {
		t.Errorf("group 0 = %+v, want git@h/web with 2 islands", groups[0])
	}
	if groups[0].islands[0].Name != "web-1" || groups[0].islands[1].Name != "web-2" {
		t.Errorf("group 0 input order not preserved: %v", groups[0].islands)
	}
	if groups[1].repo != "git@h/api" {
		t.Errorf("group 1 repo = %q, want git@h/api", groups[1].repo)
	}
	if groups[2].repo != "(no repo)" || len(groups[2].islands) != 1 {
		t.Errorf("group 2 = %+v, want (no repo) with 1 island", groups[2])
	}
}

// A non-running island can't be verified for unpushed work, so the uninstall
// pre-flight flags it without ever calling the daemon (nil client proves the
// short-circuit: a regression that fetched detail here would nil-deref).
func TestIslandAtRiskNotRunning(t *testing.T) {
	reason := islandAtRisk(context.Background(), nil, api.IslandInfo{Name: "x", Container: "hibernated"})
	if reason == "" {
		t.Fatal("expected a non-empty at-risk reason for a non-running island")
	}
}

func TestParseTags(t *testing.T) {
	got, err := parseTags([]string{"team=web", "env=staging", "flag="})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"team": "web", "env": "staging", "flag": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseTags = %v, want %v", got, want)
	}

	if _, err := parseTags([]string{"noequals"}); err == nil {
		t.Error("expected error for tag without '='")
	}
	if _, err := parseTags([]string{"=novalue"}); err == nil {
		t.Error("expected error for tag with empty key")
	}
	if got, _ := parseTags(nil); got != nil {
		t.Errorf("parseTags(nil) = %v, want nil", got)
	}
}

func TestFormatTags(t *testing.T) {
	// Sorted by key for stable output.
	if got := formatTags(map[string]string{"team": "web", "env": "prod"}); got != "env=prod team=web" {
		t.Errorf("formatTags = %q, want %q", got, "env=prod team=web")
	}
}
