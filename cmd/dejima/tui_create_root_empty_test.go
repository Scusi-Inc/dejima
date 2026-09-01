package main

import (
	"strings"
	"testing"
)

// THREE SOURCES, named by what the island STARTS FROM.
//
// The pre-scan screen offered five — start empty, scan this directory, choose
// another directory, browse GitHub, enter a URL — which is two questions
// interleaved: what should be in /workspace, and how do I go looking for it.
// The operator's framing was better: clone a repo, use a local one, or start
// with nothing. Finding is a detail inside the first two, not a peer of them.
//
// Before that it was four, with no way to start without a repo at all — and on
// Windows the two directory rows name CLIENT paths a WSL daemon cannot use, so
// none of the four led anywhere the operator could finish.
func TestCreatorOffersThreeSources(t *testing.T) {
	c := &creatorModel{rootChoices: []string{
		"Clone a repo            browse GitHub, or paste a git URL",
		"Use a local repo        a git repo already on this machine",
		"Start empty             no repo — add files later",
	}}
	if len(c.rootChoices) != 3 {
		t.Fatalf("the first screen offers %d sources, want 3", len(c.rootChoices))
	}
	for _, tc := range []struct {
		row  int
		want string
	}{
		{rootRowClone, "clone a repo"},
		{rootRowLocal, "local repo"},
		{rootRowEmpty, "start empty"},
	} {
		if tc.row >= len(c.rootChoices) {
			t.Fatalf("row constant %d is past the end of the list", tc.row)
		}
		// The constants must index the rows they name. An off-by-one runs a
		// different action than the highlighted line, and nothing looks wrong.
		if !strings.Contains(strings.ToLower(c.rootChoices[tc.row]), tc.want) {
			t.Errorf("row %d is %q, expected it to mention %q", tc.row, c.rootChoices[tc.row], tc.want)
		}
	}
}

// Enter-Enter on a fresh client must not do something surprising. The cursor's
// zero value decides the default action (#355). Clone leads because it is the
// common case AND the only one of the three that cannot be reached later from
// inside the others — but the choice is made explicitly, so reordering the rows
// cannot silently change what the default does.
func TestCreatorDefaultRowIsClone(t *testing.T) {
	if rootRowClone != 0 {
		t.Errorf("rootRowClone = %d; the cursor's zero value is what Enter-Enter runs", rootRowClone)
	}
}
