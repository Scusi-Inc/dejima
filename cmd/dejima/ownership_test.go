package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

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
