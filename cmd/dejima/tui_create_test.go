package main

import (
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func TestShortRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:o/r.git":     "github.com:o/r",
		"https://github.com/o/r.git": "github.com/o/r",
		"ssh://git@host/o/r":         "host/o/r",
	}
	for in, want := range cases {
		if got := shortRemote(in); got != want {
			t.Errorf("shortRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniqueName(t *testing.T) {
	c := &creatorModel{existing: []api.IslandInfo{
		{Name: "repo"},
		{Name: "repo-2"},
	}}
	if got := c.uniqueName("repo"); got != "repo-3" {
		t.Errorf("uniqueName collided = %q, want repo-3", got)
	}
	if got := c.uniqueName("https://github.com/o/fresh.git"); got != "fresh" {
		t.Errorf("uniqueName fresh = %q, want fresh", got)
	}
}

func TestQuoting(t *testing.T) {
	if got := shquote("a'b"); got != `'a'\''b'` {
		t.Errorf("shquote = %q", got)
	}
	if got := shquote(""); got != "''" {
		t.Errorf("shquote(empty) = %q", got)
	}
	if got := appleStr(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("appleStr = %q", got)
	}
}

func TestIslandsForRepo(t *testing.T) {
	c := &creatorModel{existing: []api.IslandInfo{
		{Name: "a", Repo: "git@github.com:o/r.git"},
		{Name: "b", Repo: "git@github.com:o/r.git"},
		{Name: "c", Repo: "git@github.com:o/other.git"},
	}}
	if n := c.islandsForRepo("git@github.com:o/r.git"); n != 2 {
		t.Errorf("islandsForRepo = %d, want 2", n)
	}
	if n := c.islandsForRepo(""); n != 0 {
		t.Errorf("islandsForRepo(empty) = %d, want 0", n)
	}
}
