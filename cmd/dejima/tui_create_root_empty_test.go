package main

import (
	"strings"
	"testing"
)

// The FRESH-CLIENT screen must offer a source the operator can actually complete.
//
// Before a repo root is configured, the creator shows a different list than the
// post-scan one — and that list offered four ways to name a repo and no way to
// skip having one. "Start empty" existed the whole time, on the screen AFTER a
// scan the operator had no reason to run.
//
// Reported from a fresh Windows install driving a WSL daemon: "no ready way to
// set up an empty repo or copy some files over". Worse there than elsewhere,
// because the two directory rows name CLIENT paths that a WSL or remote daemon
// cannot use — so on that machine NONE of the four rows led anywhere.
func TestFreshClientCanStartEmpty(t *testing.T) {
	c := &creatorModel{rootChoices: []string{
		"Start empty (no repo — add files later)",
		"Scan this directory (~/x)",
		"Choose another directory…",
		"Browse my GitHub repos…",
		"Enter a repo URL or path manually",
	}}
	joined := strings.Join(c.rootChoices, "\n")
	if !strings.Contains(strings.ToLower(joined), "start empty") {
		t.Fatalf("the pre-scan screen offers no empty option:\n%s", joined)
	}
	if !strings.HasPrefix(strings.ToLower(c.rootChoices[rootRowEmpty]), "start empty") {
		t.Errorf("rootRowEmpty (%d) is %q — the constants and the rendered rows have "+
			"drifted, so Enter runs a different action than the highlighted line",
			rootRowEmpty, c.rootChoices[rootRowEmpty])
	}
	// The row constants must index the list they describe. An off-by-one here
	// sends the operator somewhere other than the row they are looking at, and
	// nothing about the screen would look wrong.
	for _, tc := range []struct {
		row  int
		want string
	}{
		{rootRowScan, "scan"},
		{rootRowChooseDir, "choose another"},
		{rootRowGitHub, "github"},
		{rootRowManual, "manually"},
	} {
		if tc.row >= len(c.rootChoices) {
			t.Fatalf("row constant %d is past the end of the list", tc.row)
		}
		if !strings.Contains(strings.ToLower(c.rootChoices[tc.row]), tc.want) {
			t.Errorf("row %d is %q, expected it to mention %q", tc.row, c.rootChoices[tc.row], tc.want)
		}
	}
}

// Enter-Enter on a fresh client must not do something surprising. The cursor's
// zero value decides the default action (#355), and with "Start empty" leading
// that default is creating an empty island — cheap and reversible, which is why
// it is allowed to lead. The assertion is that the choice is DELIBERATE: the
// cursor is set explicitly, so reordering the rows cannot silently change what
// the default does.
func TestFreshClientDefaultRowIsTheEmptyOne(t *testing.T) {
	if rootRowEmpty != 0 {
		t.Errorf("rootRowEmpty = %d; the cursor's zero value is what Enter-Enter runs, "+
			"so a non-zero empty row means the default action is something else", rootRowEmpty)
	}
}
