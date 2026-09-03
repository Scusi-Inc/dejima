package main

import (
	"strings"
	"testing"
)

// upgradeMenuLabel returns the island settings menu's upgrade item as the menu
// actually builds it — the action name, without the muted "(…)" gloss, since
// that is the part an operator scans for. Copy that POINTS AT a menu item (the
// built-image banner) asserts against this rather than against its own duplicate
// of the string, so a rename can't leave an instruction naming an item that no
// longer exists.
func upgradeMenuLabel(t *testing.T) string {
	t.Helper()
	m := seededModel(t, island("alpha"))
	mm, ok := m.buildMenuFor(treeRow{kind: rowIsland, island: "alpha"})
	if !ok {
		t.Fatal("a running island row should have a settings menu")
	}
	for _, it := range mm.menu.items {
		if strings.HasPrefix(it.label, "Upgrade") {
			return menuActionName(it.label)
		}
	}
	t.Fatal("island settings menu should offer an Upgrade item")
	return ""
}

// The island settings menu offered the SECOND half of rolling an island onto a
// new image and not the first. `dejima upgrade` recreates a container against
// whatever image is already on the host — it never builds one — so an operator
// who found only "Upgrade" here could do the half that silently keeps the old
// image. Rebuild has to be in the same menu, and above it, because that is the
// order the two steps run in.
func TestIslandMenuOffersRebuildAboveUpgrade(t *testing.T) {
	m := seededModel(t, island("alpha"))
	mm, ok := m.buildMenuFor(treeRow{kind: rowIsland, island: "alpha"})
	if !ok {
		t.Fatal("a running island row should have a settings menu")
	}

	rebuild, upgrade := -1, -1
	for i, it := range mm.menu.items {
		switch {
		case strings.HasPrefix(it.label, "Rebuild the island image"):
			rebuild = i
		case strings.HasPrefix(it.label, "Upgrade"):
			upgrade = i
		}
	}
	if rebuild < 0 {
		t.Fatalf("island settings menu must offer a rebuild item; items=%+v", mm.menu.items)
	}
	if upgrade < 0 {
		t.Fatalf("island settings menu should still offer the upgrade item; items=%+v", mm.menu.items)
	}
	if rebuild > upgrade {
		t.Errorf("rebuild is the step BEFORE upgrade and must be listed above it (rebuild=%d upgrade=%d)", rebuild, upgrade)
	}

	// Same accelerator the dashboard has always used. A NEW letter here would be
	// the fifth meaning for some existing key; reusing `b` adds a place to find
	// the action rather than another thing a key can mean.
	if got := mm.menu.items[rebuild].key; got != "b" {
		t.Errorf("rebuild item should carry the existing [b] accelerator, got %q", got)
	}

	// And choosing it arms the real confirm — the menu must not be a decoration
	// that names an action it can't start.
	mm.menu.sel = rebuild
	res, _ := mm.actionMenuKey(key("enter"))
	got := res.(tuiModel)
	if got.confirm == nil || got.confirm.verb != "build-image" {
		t.Fatalf("selecting rebuild should arm build-image, got %+v", got.confirm)
	}
}

// While a build is in flight the `b` handler no-ops. A menu row that stays
// selectable and then does nothing when chosen is indistinguishable from a
// keypress that didn't register, so the row goes disabled and says why.
func TestRebuildItemIsDisabledWhileBuilding(t *testing.T) {
	m := seededModel(t, island("alpha"))
	m.building = true
	mm, ok := m.buildMenuFor(treeRow{kind: rowIsland, island: "alpha"})
	if !ok {
		t.Fatal("a running island row should have a settings menu")
	}
	var found bool
	for _, it := range mm.menu.items {
		if !strings.Contains(it.label, "island image") || strings.HasPrefix(it.label, "Upgrade") {
			continue
		}
		found = true
		if !it.disabled {
			t.Errorf("rebuild row must be disabled while a build is in flight; got %+v", it)
		}
		if !strings.Contains(it.label, "in progress") {
			t.Errorf("a disabled rebuild row must say why, got %q", it.label)
		}
	}
	if !found {
		t.Fatalf("the rebuild row should still be shown while building; items=%+v", mm.menu.items)
	}

	// The Server menu [H] shares the row, so it can't disagree about the state.
	sm := m.openServerMenu()
	for _, it := range sm.menu.items {
		if strings.Contains(it.label, "island image") && !it.disabled {
			t.Errorf("server menu's rebuild row must be disabled while building too, got %+v", it)
		}
	}
}
