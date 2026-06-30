package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/spawn"
)

// TestSpawnGrantApplyLoaded: a granted response maps onto the form's preset
// selections (and the used count), while a no-grant response is the deny-all
// default.
func TestSpawnGrantApplyLoaded(t *testing.T) {
	ed := &spawnGrantEditor{island: "isl", loading: true}
	ed.applyLoaded(spawnGrantLoadedMsg{island: "isl", resp: &api.SpawnGrantResponse{
		Granted: true,
		Grant: &spawn.Grant{
			MaxConcurrent: 4, MaxTotal: 10, TTL: time.Hour, PerAgentMemory: "512m", Used: 3,
		},
	}})
	if !ed.granted || ed.used != 3 {
		t.Fatalf("granted=%v used=%d, want true/3", ed.granted, ed.used)
	}
	if spawnConcPresets[ed.concSel].value != 4 {
		t.Errorf("concurrent preset = %d, want 4", spawnConcPresets[ed.concSel].value)
	}
	if spawnTotalPresets[ed.totalSel].value != 10 {
		t.Errorf("total preset = %d, want 10", spawnTotalPresets[ed.totalSel].value)
	}
	if spawnTTLPresets[ed.ttlSel].value != "1h" {
		t.Errorf("ttl preset = %q, want 1h", spawnTTLPresets[ed.ttlSel].value)
	}
	if spawnMemPresets[ed.memSel].value != "512m" {
		t.Errorf("mem preset = %q, want 512m", spawnMemPresets[ed.memSel].value)
	}

	ed2 := &spawnGrantEditor{island: "isl", loading: true}
	ed2.applyLoaded(spawnGrantLoadedMsg{island: "isl", resp: &api.SpawnGrantResponse{Granted: false}})
	if ed2.granted {
		t.Error("a no-grant response should leave granted=false (deny-all)")
	}
}

// TestSpawnGrantPresetHelpers covers the value→preset-index mappers, incl. the
// CLI-set-outside-presets fallback to index 0.
func TestSpawnGrantPresetHelpers(t *testing.T) {
	if i := nearestIntPreset(8, spawnConcPresets); spawnConcPresets[i].value != 8 {
		t.Errorf("nearestIntPreset(8) → %d", spawnConcPresets[i].value)
	}
	if i := nearestIntPreset(7, spawnConcPresets); i != 0 {
		t.Errorf("a non-preset concurrency should fall back to index 0, got %d", i)
	}
	if i := durationPresetIndex(30 * time.Minute); spawnTTLPresets[i].value != "30m" {
		t.Errorf("durationPresetIndex(30m) → %q", spawnTTLPresets[i].value)
	}
	if i := durationPresetIndex(0); i != 0 {
		t.Errorf("durationPresetIndex(0) should be the 'no cap' row, got %d", i)
	}
	if i := durationPresetIndex(2 * time.Hour); i != 0 {
		t.Errorf("a non-preset TTL should fall back to 'no cap', got %d", i)
	}
	if i := strPresetIndex("1g", spawnMemPresets); spawnMemPresets[i].value != "1g" {
		t.Errorf("strPresetIndex(1g) → %q", spawnMemPresets[i].value)
	}
}

func spawnGrantModel(ed *spawnGrantEditor) tuiModel {
	m := initialTUIModel(nil)
	m.spawnGrant = ed
	return m
}

// TestSpawnGrantEnterApplies: with a positive concurrency, Enter fires an apply
// (set) command and marks the form busy.
func TestSpawnGrantEnterApplies(t *testing.T) {
	m := spawnGrantModel(&spawnGrantEditor{island: "isl", concSel: 2}) // value 2
	out, cmd := m.spawnGrantKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(tuiModel)
	if cmd == nil || !m.spawnGrant.busy {
		t.Errorf("enter with concurrency>0 should apply (busy=%v cmd=%v)", m.spawnGrant.busy, cmd != nil)
	}
}

// TestSpawnGrantOffRevokesWhenGranted: choosing "off" concurrency and applying
// revokes an existing grant; on a not-granted island it's a clean no-op (closes).
func TestSpawnGrantOffRevokesWhenGranted(t *testing.T) {
	m := spawnGrantModel(&spawnGrantEditor{island: "isl", concSel: 0, granted: true}) // "off"
	out, cmd := m.spawnGrantKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !out.(tuiModel).spawnGrant.busy {
		t.Error("off + enter on a granted island should revoke")
	}

	m = spawnGrantModel(&spawnGrantEditor{island: "isl", concSel: 0, granted: false})
	out, _ = m.spawnGrantKey(tea.KeyMsg{Type: tea.KeyEnter})
	if out.(tuiModel).spawnGrant != nil {
		t.Error("off + enter on a not-granted island should just close (no-op)")
	}
}

// TestSpawnGrantRevokeKey: x revokes when granted, and is a no-op otherwise.
func TestSpawnGrantRevokeKey(t *testing.T) {
	m := spawnGrantModel(&spawnGrantEditor{island: "isl", granted: true})
	out, cmd := m.spawnGrantKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd == nil || !out.(tuiModel).spawnGrant.busy {
		t.Error("x on a granted island should revoke")
	}

	m = spawnGrantModel(&spawnGrantEditor{island: "isl", granted: false})
	_, cmd = m.spawnGrantKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd != nil {
		t.Error("x on a not-granted island should be a no-op")
	}
}

// TestSpawnGrantCycle: ←/→ cycles the focused field's preset and wraps.
func TestSpawnGrantCycle(t *testing.T) {
	ed := &spawnGrantEditor{field: sgConcurrent, concSel: 0}
	ed.cycle(-1) // wrap backwards from 0
	if ed.concSel != len(spawnConcPresets)-1 {
		t.Errorf("cycle(-1) from 0 should wrap to last, got %d", ed.concSel)
	}
	ed.cycle(1) // wrap forward back to 0
	if ed.concSel != 0 {
		t.Errorf("cycle(+1) should wrap to 0, got %d", ed.concSel)
	}
}
