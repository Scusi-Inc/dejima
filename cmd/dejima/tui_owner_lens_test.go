package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

func islandNamesOf(islands []api.IslandInfo) []string {
	out := make([]string, len(islands))
	for i, isl := range islands {
		out[i] = isl.Name
	}
	return out
}

func ownerLensModel() tuiModel {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 40
	m.islands = []api.IslandInfo{
		{Name: "web", Owner: "amanda", Container: "running"},
		{Name: "api", Owner: "aoos", Container: "running"},
		{Name: "docs", Owner: "amanda", Container: "running"},
	}
	return m
}

// TestOwnedIslandsFiltersByCaller: in the your-islands lens with a known caller,
// only the caller's islands show; the all lens shows everything.
func TestOwnedIslandsFiltersByCaller(t *testing.T) {
	m := ownerLensModel()
	m.callerOwner = "amanda"

	m.ownerLens = lensOwn
	if got := islandNamesOf(m.ownedIslands()); !eq(got, []string{"web", "docs"}) {
		t.Errorf("own lens should show amanda's islands, got %v", got)
	}

	m.ownerLens = lensAll
	if got := m.ownedIslands(); len(got) != 3 {
		t.Errorf("all lens should show every island, got %d", len(got))
	}
}

// TestOwnedIslandsFailsOpen: before the daemon reports the caller's owner id
// (callerOwner == ""), the lens never hides anything — it can only narrow once we
// know who "you" are.
func TestOwnedIslandsFailsOpen(t *testing.T) {
	m := ownerLensModel() // callerOwner == ""
	m.ownerLens = lensOwn
	if got := m.ownedIslands(); len(got) != 3 {
		t.Errorf("unknown caller owner must fail open (show all), got %d", len(got))
	}
}

// TestToggleOwnerLensReanchors: flipping the lens keeps the cursor on the same
// island when it's still visible.
func TestToggleOwnerLensReanchors(t *testing.T) {
	m := ownerLensModel()
	m.callerOwner = "amanda"
	m.ownerLens = lensAll
	// Put the cursor on "api" (aoos's) — visible in the all lens.
	for i, r := range m.visibleRows() {
		if r.kind == rowIsland && r.island == "api" {
			m.selected = i
		}
	}
	// Toggling to the your-islands lens hides "api"; selection must clamp into the
	// shorter list rather than dangle out of range.
	m = m.toggleOwnerLens()
	if m.ownerLens != lensOwn {
		t.Fatalf("toggle should switch to lensOwn, got %d", m.ownerLens)
	}
	if m.selected < 0 || m.selected >= len(m.visibleRows()) {
		t.Errorf("selection %d out of range after toggle (rows=%d)", m.selected, len(m.visibleRows()))
	}
}

// TestOwnerKeyTogglesLens: for the host owner, `O` flips the lens and notices.
func TestOwnerKeyTogglesLens(t *testing.T) {
	m := ownerLensModel()
	m.callerOwner, m.callerRole = "amanda", "owner"
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("O")})
	m = out.(tuiModel)
	if m.ownerLens != lensAll {
		t.Errorf("O should switch to lensAll, got %d", m.ownerLens)
	}
	if !strings.Contains(m.lastNotice, "ALL islands") {
		t.Errorf("O should note the all-islands view, got %q", m.lastNotice)
	}
}

// TestOwnerKeyGatedForNonOwner: a teammate (non-owner) can't toggle — their list
// is already server-filtered — and an ownership-unaware daemon is inert.
func TestOwnerKeyGatedForNonOwner(t *testing.T) {
	// Teammate: known owner, operator role → no toggle, explanatory notice.
	m := ownerLensModel()
	m.callerOwner, m.callerRole = "amanda", "operator"
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("O")})
	m = out.(tuiModel)
	if m.ownerLens != lensOwn {
		t.Errorf("a non-owner must not flip the lens, got %d", m.ownerLens)
	}
	if !strings.Contains(m.lastNotice, "your own islands") {
		t.Errorf("a non-owner should get an explanatory notice, got %q", m.lastNotice)
	}

	// Ownership-unaware daemon: no caller identity → inert with a hint.
	m = ownerLensModel() // callerOwner == "", callerRole == ""
	out, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("O")})
	if out.(tuiModel).ownerLens != lensOwn {
		t.Error("O must be inert when the daemon reports no ownership")
	}
}

// TestOverviewPopulatesCallerIdentity: each overview poll refreshes the caller's
// own owner id + role from the daemon (drives the lens + its gate).
func TestOverviewPopulatesCallerIdentity(t *testing.T) {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 40
	// gen 0 matches a fresh model's generation, so this isn't discarded as stale.
	out, _ := m.Update(overviewMsg{ov: &api.OverviewResponse{Owner: "aoos", Role: "owner"}})
	m = out.(tuiModel)
	if m.callerOwner != "aoos" || m.callerRole != "owner" {
		t.Errorf("overview should populate caller identity, got owner=%q role=%q", m.callerOwner, m.callerRole)
	}
}

// TestOwnerTagShownInAllLens: island rows carry an @owner tag in the all lens,
// and don't in the your-islands lens.
func TestOwnerTagShownInAllLens(t *testing.T) {
	m := ownerLensModel()
	m.callerOwner = "amanda"

	m.ownerLens = lensAll
	body, _ := m.renderList(80)
	if !strings.Contains(plain(body), "@aoos") {
		t.Errorf("all lens should tag rows with @owner:\n%s", plain(body))
	}

	m.ownerLens = lensOwn
	body, _ = m.renderList(80)
	if strings.Contains(plain(body), "@amanda") {
		t.Errorf("your-islands lens should not tag rows with @owner:\n%s", plain(body))
	}
}
