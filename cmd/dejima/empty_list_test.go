package main

import (
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// An empty island list is not proof of an empty fleet.
//
// The overview counts the same projects the list enumerates, so the two
// disagreeing means the LIST is wrong. Rendering that as "Set up your first
// island" tells an operator with a running fleet that they have none.
//
// It happened. A daemon restarted mid-update served an empty list for a moment;
// listMsg applies a successful reply and clears lastError, so the empty array
// arrived looking exactly like a fresh install — first-run prompt, no error —
// while two islands were still running on the host and the footer still read
// "2 islands · 2 running". The operator read it as having lost everything, and
// nothing on screen contradicted that.
func TestEmptyListWithANonEmptyOverviewSaysSo(t *testing.T) {
	m := tuiModel{
		width: 100, height: 40,
		islands:  nil,
		overview: &api.OverviewResponse{TotalIslands: 2, Running: 2},
	}
	out, _ := m.renderList(90)

	if strings.Contains(out, "Set up your first island") {
		t.Fatal("a fleet of 2 was rendered as a first-run prompt — the operator is " +
			"told to create their first island while two are running")
	}
	if !strings.Contains(out, "2 island") {
		t.Errorf("the contradiction is not stated:\n%s", out)
	}
	// It must say nothing was lost. That is the actual question in the operator's
	// head, and leaving it unanswered is what turns a transient into an incident.
	if !strings.Contains(out, "Nothing has been deleted") {
		t.Errorf("the pane does not address the obvious fear:\n%s", out)
	}
}

// The genuine first-run case must be untouched: no overview, or an overview that
// agrees there are none, still gets the welcome prompt.
//
// Without this control the guard above could be "never show the first-run pane",
// which breaks the actual first run — a worse bug than the one being fixed.
func TestGenuineFirstRunStillWelcomes(t *testing.T) {
	for name, ov := range map[string]*api.OverviewResponse{
		"no overview yet":    nil,
		"overview agrees: 0": {TotalIslands: 0},
	} {
		m := tuiModel{width: 100, height: 40, overview: ov}
		out, _ := m.renderList(90)
		if !strings.Contains(out, "Set up your first island") {
			t.Errorf("%s: a real first run lost its welcome prompt:\n%s", name, out)
		}
		if strings.Contains(out, "Nothing has been deleted") {
			t.Errorf("%s: a fresh install was warned about a fleet it does not have", name)
		}
	}
}
