package main

import (
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/teatest"

	"github.com/aoos/dejima/internal/api"
)

// TestTUIScopeDenyAll: P opens the Port scope-picker; with no scopes it shows the
// deny-all banner (the same wording as the CLI), making the security posture
// visible. esc closes it.
func TestTUIScopeDenyAll(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")

	tm.Send(key("P"))
	// The real load (no daemon) fails fast → empty scopes → deny-all banner.
	waitForAll(t, tm, "Port scopes", "deny-all: no host files reachable")

	tm.Send(key("esc"))
	tm.Send(key("q"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second)).(tuiModel)
	if final.scope != nil {
		t.Errorf("scope picker should be closed after esc")
	}
}

// TestTUIScopeListsGrants: a fed scopes-loaded message renders the granted host
// paths and their modes in the picker.
func TestTUIScopeListsGrants(t *testing.T) {
	tm := runModel(t, seededModel(t, island("alpha")))
	waitForAll(t, tm, "alpha")

	tm.Send(key("P"))
	waitForAll(t, tm, "Port scopes")

	tm.Send(scopesLoadedMsg{island: "alpha", resp: &api.PortScopesResponse{
		Scopes: []api.PortScopeView{
			{Name: "vault", HostPath: "/Users/me/Obsidian/Vault", Mode: "ro", GrantedAt: time.Now()},
			{Name: "out", HostPath: "/Users/me/exports", Mode: "rw", GrantedAt: time.Now()},
		},
	}})
	waitForAll(t, tm, "vault", "/Users/me/Obsidian/Vault", "rw")

	tm.Send(key("esc"))
	tm.Send(key("q"))
	tm.FinalModel(t, teatest.WithFinalTimeout(8*time.Second))
}

// TestTUIScopeAddForm: `a` opens the add sub-form; typing builds the host path
// and ←/→ flips the :ro/:rw mode. A relative path is refused before any request.
func TestTUIScopeAddForm(t *testing.T) {
	m := seededModel(t, island("alpha"))
	mm, _ := m.openScopeView("alpha")
	m = mm.(tuiModel)
	m.scope.loading = false // pretend the load completed (no daemon in the test)

	// Open the add form.
	mm, _ = m.handleKey(key("a"))
	m = mm.(tuiModel)
	if !m.scope.adding {
		t.Fatalf("`a` should open the add form")
	}

	// Type a relative path, then enter → refused locally, still in the form.
	for _, ch := range "rel/path" {
		mm, _ = m.handleKey(key(string(ch)))
		m = mm.(tuiModel)
	}
	mm, _ = m.handleKey(key("enter"))
	m = mm.(tuiModel)
	if m.scope.busy {
		t.Errorf("a relative path must not dispatch a grant")
	}
	if m.scope.errText == "" {
		t.Errorf("a relative path should surface an error")
	}

	// Flip to :rw.
	if m.scope.modeRW {
		t.Fatalf("mode should default to :ro")
	}
	mm, _ = m.handleKey(key("right"))
	m = mm.(tuiModel)
	if !m.scope.modeRW {
		t.Errorf("←/→ should flip the mode to :rw")
	}
}

// TestTUIScopeAddAbsoluteDispatches: an absolute path + enter dispatches a grant
// (busy goes true and a command is returned).
func TestTUIScopeAddAbsoluteDispatches(t *testing.T) {
	m := seededModel(t, island("alpha"))
	mm, _ := m.openScopeView("alpha")
	m = mm.(tuiModel)
	m.scope.loading = false
	mm, _ = m.handleKey(key("a"))
	m = mm.(tuiModel)
	for _, ch := range "/srv/data" {
		mm, _ = m.handleKey(key(string(ch)))
		m = mm.(tuiModel)
	}
	mm, cmd := m.handleKey(key("enter"))
	m = mm.(tuiModel)
	if !m.scope.busy {
		t.Errorf("an absolute path should dispatch a grant (busy=true)")
	}
	if m.scope.adding {
		t.Errorf("the add form should close on submit")
	}
	if cmd == nil {
		t.Errorf("expected a grant command to be dispatched")
	}
}

// TestTUIScopeRevokeConfirm: with a scope selected, `d` arms a y/n revoke; `y`
// dispatches the revoke (busy), and any other key cancels it.
func TestTUIScopeRevokeConfirm(t *testing.T) {
	m := seededModel(t, island("alpha"))
	mm, _ := m.openScopeView("alpha")
	m = mm.(tuiModel)
	m.scope.applyLoaded(scopesLoadedMsg{island: "alpha", resp: &api.PortScopesResponse{
		Scopes: []api.PortScopeView{{Name: "vault", HostPath: "/v", Mode: "ro", GrantedAt: time.Now()}},
	}})

	// `d` arms the confirmation; a non-y key cancels it without dispatching.
	mm, _ = m.handleKey(key("d"))
	m = mm.(tuiModel)
	if m.scope.confirmRevoke != "vault" {
		t.Fatalf("`d` should arm a revoke confirm for the selected scope, got %q", m.scope.confirmRevoke)
	}
	mm, _ = m.handleKey(key("n"))
	m = mm.(tuiModel)
	if m.scope.confirmRevoke != "" || m.scope.busy {
		t.Errorf("`n` should cancel the revoke without dispatching")
	}

	// Re-arm and confirm with `y` → dispatches.
	mm, _ = m.handleKey(key("d"))
	m = mm.(tuiModel)
	mm, cmd := m.handleKey(key("y"))
	m = mm.(tuiModel)
	if !m.scope.busy {
		t.Errorf("`y` should dispatch the revoke (busy=true)")
	}
	if cmd == nil {
		t.Errorf("expected a revoke command to be dispatched")
	}
}

// TestTUIScopeMutationError: a failed grant/revoke surfaces the daemon's error
// (e.g. a viewer's 403) in the picker rather than silently no-op'ing.
func TestTUIScopeMutationError(t *testing.T) {
	m := seededModel(t, island("alpha"))
	mm, _ := m.openScopeView("alpha")
	m = mm.(tuiModel)
	m.scope.busy = true

	mm, _ = m.Update(scopeMutatedMsg{island: "alpha", verb: "grant", err: errForbidden{}})
	m = mm.(tuiModel)
	if m.scope.busy {
		t.Errorf("an error should clear the busy flag")
	}
	if m.scope.errText == "" {
		t.Errorf("a mutation error should be surfaced in the picker")
	}
}

type errForbidden struct{}

func (errForbidden) Error() string { return "403 forbidden: operator role required" }
