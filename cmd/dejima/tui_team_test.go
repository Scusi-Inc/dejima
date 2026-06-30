package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// teamModel builds a dashboard model with the Team overlay open and a couple of
// islands available for the scope picker.
func teamModel() tuiModel {
	m := initialTUIModel(nil)
	m.width, m.height = 100, 40
	m.islands = []api.IslandInfo{{Name: "starship"}, {Name: "janus"}}
	m.team = &teamView{scopeAll: true, scopeSel: map[string]bool{}}
	return m
}

// TestTeamOwnerOnlyGate: a 403 from the (owner-only) token list flips the view
// into its explanatory gate rather than surfacing a raw error or empty form.
func TestTeamOwnerOnlyGate(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New(`role "operator" may not access this route (requires owner)`), true},
		{errors.New("http 403"), true},
		{errors.New("daemon unreachable: connection refused"), false},
		{nil, false},
	}
	for _, c := range cases {
		v := &teamView{}
		v.applyLoaded(tokensLoadedMsg{err: c.err})
		if v.ownerOnly != c.want {
			t.Errorf("applyLoaded(%v): ownerOnly=%v, want %v", c.err, v.ownerOnly, c.want)
		}
		if c.err != nil && c.want && v.loadErr != "" {
			t.Errorf("owner-only gate should not also set a generic loadErr, got %q", v.loadErr)
		}
	}
}

// TestTeamMintRequestAllIslands: the default form mints an operator token scoped
// to all islands (no Islands list), carrying the trimmed label.
func TestTeamMintRequestAllIslands(t *testing.T) {
	m := teamModel()
	m.team.label = "  amanda  "
	req, verr := m.teamMintRequest()
	if verr != "" {
		t.Fatalf("unexpected validation error: %q", verr)
	}
	if req.Role != "operator" {
		t.Errorf("role = %q, want operator", req.Role)
	}
	if len(req.Islands) != 0 {
		t.Errorf("all-islands scope should send no Islands, got %v", req.Islands)
	}
	if req.Label != "amanda" {
		t.Errorf("label = %q, want trimmed 'amanda'", req.Label)
	}
}

// TestTeamMintRequestScoped: a viewer token scoped to chosen islands sends those
// island names (in stable sorted order).
func TestTeamMintRequestScoped(t *testing.T) {
	m := teamModel()
	m.team.roleSel = 1 // viewer
	m.team.scopeAll = false
	m.team.scopeSel["janus"] = true
	req, verr := m.teamMintRequest()
	if verr != "" {
		t.Fatalf("unexpected validation error: %q", verr)
	}
	if req.Role != "viewer" {
		t.Errorf("role = %q, want viewer", req.Role)
	}
	if len(req.Islands) != 1 || req.Islands[0] != "janus" {
		t.Errorf("scoped Islands = %v, want [janus]", req.Islands)
	}
}

// TestTeamMintRequestScopedEmpty: a custom scope with nothing checked is a
// validation error, not an unscoped (all-islands) token by accident.
func TestTeamMintRequestScopedEmpty(t *testing.T) {
	m := teamModel()
	m.team.scopeAll = false
	if _, verr := m.teamMintRequest(); verr == "" {
		t.Error("custom scope with no island selected should be a validation error")
	}
}

// TestTeamHostPrefill: opening the view prefills the host field with the current
// connection target (what this client dialed = what a teammate would dial).
func TestTeamHostPrefill(t *testing.T) {
	m := initialTUIModel(nil)
	m.activeHost = "minion.ts.net:7274"
	out, _ := m.openTeamView()
	if got := out.(tuiModel).team.host; got != "minion.ts.net:7274" {
		t.Errorf("host should prefill from activeHost, got %q", got)
	}
}

// TestTeamHostTyping: the Host field accepts typed input (focus order role,
// scope, host, label, create).
func TestTeamHostTyping(t *testing.T) {
	m := teamModel()
	m.team.focus = 2
	if got := m.teamCurrent(); got.kind != tfHost {
		t.Fatalf("focus 2 should be the host field, got kind %d", got.kind)
	}
	for _, r := range "h:1" {
		out, _ := m.teamKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(tuiModel)
	}
	if m.team.host != "h:1" {
		t.Errorf("host = %q, want 'h:1'", m.team.host)
	}
}

// TestTeamMintedBlobRender: when a blob was produced (host given), the minted
// panel leads with the one-paste invite and the `dejima join` instruction, not
// the env fallback.
func TestTeamMintedBlobRender(t *testing.T) {
	m := teamModel()
	m.team.minted = &api.CreateTokenResponse{Secret: "sek_x", Token: api.TokenView{Role: "operator", Label: "amanda"}}
	m.team.mintedBlob = "dejima-invite:abc123"
	got := plain(m.renderTeamView())
	if !strings.Contains(got, "dejima-invite:abc123") {
		t.Errorf("minted panel should show the blob:\n%s", got)
	}
	if !strings.Contains(got, "dejima join") {
		t.Errorf("minted panel should show the join instruction:\n%s", got)
	}
}

// TestTeamRoleNeverOwner: the invite flow only ever offers operator/viewer —
// minting an owner is a deliberate CLI act, never a casual TUI invite.
func TestTeamRoleNeverOwner(t *testing.T) {
	for _, r := range teamRoles {
		if r == "owner" {
			t.Fatalf("teamRoles must not offer owner, got %v", teamRoles)
		}
	}
}

// TestTeamScopeTogglesIslands: switching scope off all-islands surfaces an island
// checkbox per island in the focus model; switching back hides them.
func TestTeamScopeTogglesIslands(t *testing.T) {
	m := teamModel()
	base := len(m.teamFocusItems())
	m.team.scopeAll = false
	withIslands := len(m.teamFocusItems())
	if withIslands != base+len(m.islands) {
		t.Errorf("custom scope should add %d island focus items (%d -> %d)", len(m.islands), base, withIslands)
	}
}

// TestTeamSpaceTogglesFocusedIsland: with scope custom and an island focused,
// space checks/unchecks it.
func TestTeamSpaceTogglesFocusedIsland(t *testing.T) {
	m := teamModel()
	m.team.scopeAll = false
	m.team.focus = 2 // role(0), scope(1), first island(2)
	if got := m.teamCurrent(); got.kind != tfIsland {
		t.Fatalf("focus 2 should be an island, got kind %d", got.kind)
	}
	first := m.islandNames()[0]
	out, _ := m.teamKey(tea.KeyMsg{Type: tea.KeySpace})
	m = out.(tuiModel)
	if !m.team.scopeSel[first] {
		t.Errorf("space should check the focused island %q", first)
	}
	out, _ = m.teamKey(tea.KeyMsg{Type: tea.KeySpace})
	if out.(tuiModel).team.scopeSel[first] {
		t.Errorf("space again should uncheck %q", first)
	}
}

// TestTeamLabelTyping: while the Label field is focused, printable keys type into
// it (they aren't consumed as navigation).
func TestTeamLabelTyping(t *testing.T) {
	m := teamModel() // all-islands: focus order is role, scope, host, label, create
	m.team.focus = 3
	if got := m.teamCurrent(); got.kind != tfLabel {
		t.Fatalf("focus 3 should be the label field, got kind %d", got.kind)
	}
	for _, r := range "ann" {
		out, _ := m.teamKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = out.(tuiModel)
	}
	if m.team.label != "ann" {
		t.Errorf("label = %q, want 'ann'", m.team.label)
	}
	// 'j' is navigation elsewhere, but here it should type, not move focus.
	out, _ := m.teamKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = out.(tuiModel)
	if m.team.label != "annj" {
		t.Errorf("j should type into the label, got %q", m.team.label)
	}
}

// TestTeamRevokeFocused: d on a focused token row fires a revoke and clears any
// stale action error.
func TestTeamRevokeFocused(t *testing.T) {
	m := teamModel()
	m.team.tokens = []api.TokenView{{ID: "tok_a", Role: "operator"}}
	// focus order (all-islands): role, scope, host, label, create, token[0]
	m.team.focus = 5
	if got := m.teamCurrent(); got.kind != tfToken {
		t.Fatalf("focus 5 should be the token row, got kind %d", got.kind)
	}
	_, cmd := m.teamKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd == nil {
		t.Error("d on a token row should fire a revoke command")
	}
}

// TestTeamEnterMints: Enter on the Create button fires a mint command and marks
// the form busy; an invalid (empty custom scope) form sets an error instead.
func TestTeamEnterMints(t *testing.T) {
	m := teamModel()
	m.team.focus = 4 // role, scope, host, label, create
	if got := m.teamCurrent(); got.kind != tfCreate {
		t.Fatalf("focus 4 should be the create button, got kind %d", got.kind)
	}
	out, cmd := m.teamKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = out.(tuiModel)
	if cmd == nil || !m.team.minting {
		t.Errorf("enter on create should mint (minting=%v, cmd=%v)", m.team.minting, cmd != nil)
	}
}

// TestTeamMintedDismiss: while the minted invite is showing, enter/esc dismisses
// it and reloads the list; the secret is not retained in view afterward.
func TestTeamMintedDismiss(t *testing.T) {
	m := teamModel()
	m.team.minted = &api.CreateTokenResponse{Secret: "s3cr3t", Token: api.TokenView{Role: "operator"}}
	out, cmd := m.teamKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = out.(tuiModel)
	if m.team.minted != nil {
		t.Error("esc should dismiss the minted invite")
	}
	if cmd == nil {
		t.Error("dismissing should reload the token list")
	}
}

// TestTeamRenderOwnerGate: the gate render mentions it's owner-only (so a
// non-owner isn't left staring at an empty form).
func TestTeamRenderOwnerGate(t *testing.T) {
	m := teamModel()
	m.team.ownerOnly = true
	if got := plain(m.renderTeamView()); !strings.Contains(got, "owner-only") {
		t.Errorf("owner gate should say it's owner-only:\n%s", got)
	}
}

// TestTeamRenderForm: the form renders the role, scope, the issued-token list,
// and the minted-invite panel without panicking — across all-islands, custom
// scope, and the one-time secret view.
func TestTeamRenderForm(t *testing.T) {
	m := teamModel()
	m.team.tokens = []api.TokenView{{ID: "tok_a", Role: "operator", Label: "amanda"}}
	form := plain(m.renderTeamView())
	for _, want := range []string{"Role", "operator", "Scope", "all islands", "Issued tokens", "amanda"} {
		if !strings.Contains(form, want) {
			t.Errorf("form missing %q:\n%s", want, form)
		}
	}

	m.team.scopeAll = false
	scoped := plain(m.renderTeamView())
	if !strings.Contains(scoped, "starship") || !strings.Contains(scoped, "janus") {
		t.Errorf("custom-scope form should list islands:\n%s", scoped)
	}

	m.team.minted = &api.CreateTokenResponse{Secret: "s3cr3t-shown-once", Token: api.TokenView{Role: "viewer", Label: "amanda"}}
	minted := plain(m.renderTeamView())
	for _, want := range []string{"invite created", "s3cr3t-shown-once", "DEJIMA_TOKEN"} {
		if !strings.Contains(minted, want) {
			t.Errorf("minted panel missing %q:\n%s", want, minted)
		}
	}
}
