package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
)

// ghModel builds a TUI sitting on the grants pane with the given credential
// state already loaded. (grantsModelWith, in tui_grants_test.go, takes a whole
// response; this wraps it for the credential-focused cases.)
func ghModel(gh api.HostGitHubCredentialView, other ...func(*api.IslandGrantsResponse)) tuiModel {
	resp := &api.IslandGrantsResponse{HostGitHub: gh}
	for _, f := range other {
		f(resp)
	}
	m := grantsModelWith(resp)
	m.grants.island = "proj"
	return m
}

// The three states must be distinguishable on sight. This is the same
// "couldn't determine vs determined-and-fine" problem the doctor check had,
// on a different surface: if deny and grant render alike, the pane stops
// carrying information.
func TestGrantsViewDistinguishesCredentialStates(t *testing.T) {
	granted := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		gh       api.HostGitHubCredentialView
		want     []string
		notWant  []string
		wantKeys string
	}{
		{
			name:     "denied is the default and reads as a fact",
			gh:       api.HostGitHubCredentialView{Eligible: true},
			want:     []string{"no GitHub credential of its own", "clone/push of a private repo will fail"},
			notWant:  []string{"EVERY private repo", "grandfathered"},
			wantKeys: "[G] grant host GitHub credential",
		},
		{
			name:     "a deliberate grant names who and when",
			gh:       api.HostGitHubCredentialView{Eligible: true, Granted: true, GrantedBy: "alice", GrantedAt: granted},
			want:     []string{"EVERY private repo", "granted by alice"},
			notWant:  []string{"grandfathered", "no GitHub credential of its own"},
			wantKeys: "[G] revoke host GitHub credential",
		},
		{
			name:     "grandfathered says nobody has decided",
			gh:       api.HostGitHubCredentialView{Eligible: true, Granted: true, Grandfathered: true, GrantedAt: granted},
			want:     []string{"grandfathered", "not yet decided"},
			notWant:  []string{"granted by"},
			wantKeys: "[G] revoke host GitHub credential",
		},
		{
			name: "a tenant island is n/a, not denied",
			gh:   api.HostGitHubCredentialView{Eligible: false},
			// "denied" would imply a grant is the missing piece; for a tenant it
			// isn't, and the daemon would refuse it.
			want:     []string{"own GitHub identity", "dejima github connect"},
			notWant:  []string{"[G] grant", "no GitHub credential of its own"},
			wantKeys: "[esc] close",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Force the full pane (not the fully-contained short form) so the
			// section itself is exercised.
			m := ghModel(c.gh, func(r *api.IslandGrantsResponse) {
				r.Port = []api.PortScopeView{{Name: "vault", HostPath: "/tmp/v", Mode: "ro"}}
			})
			out := m.renderGrantsView()
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in:\n%s", w, out)
				}
			}
			for _, n := range c.notWant {
				if strings.Contains(out, n) {
					t.Errorf("should not contain %q in:\n%s", n, out)
				}
			}
			if !strings.Contains(out, c.wantKeys) {
				t.Errorf("missing action hint %q in:\n%s", c.wantKeys, out)
			}
		})
	}
}

// An island holding the host login and nothing else is NOT fully contained.
// This is the claim the pane makes most loudly, and after #334 it was wrong:
// the containment total counted Port/MCP/Links/Capabilities and ignored the
// widest-reaching grant of the five.
func TestGrantsViewFullyContainedAccountsForHostGH(t *testing.T) {
	m := ghModel(api.HostGitHubCredentialView{Eligible: true, Granted: true, GrantedAt: time.Now()})
	out := m.renderGrantsView()
	if strings.Contains(out, "fully contained") {
		t.Errorf("an island holding the host's account-wide login is not fully contained:\n%s", out)
	}
	if !strings.Contains(out, "EVERY private repo") {
		t.Errorf("the grant must be shown:\n%s", out)
	}

	// The genuinely contained case still says so — and says the GitHub part out
	// loud rather than leaving it to be inferred from absence.
	m = ghModel(api.HostGitHubCredentialView{Eligible: true})
	out = m.renderGrantsView()
	if !strings.Contains(out, "fully contained") {
		t.Errorf("a deny-all island should still read as contained:\n%s", out)
	}
	if !strings.Contains(out, "no GitHub credential") {
		t.Errorf("the contained summary should state the GitHub position, not omit it:\n%s", out)
	}
	if !strings.Contains(out, "[G] grant host GitHub credential") {
		t.Errorf("the contained view is exactly where an operator looks for how to grant:\n%s", out)
	}
}

// Granting the host operator's whole account must not happen on a stray
// keypress.
func TestGrantsViewGrantRequiresConfirmation(t *testing.T) {
	m := ghModel(api.HostGitHubCredentialView{Eligible: true})

	next, cmd := m.grantsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = next.(tuiModel)
	if cmd != nil {
		t.Fatal("G must not act immediately — it should only arm the confirmation")
	}
	if m.grants.pending != "grant" {
		t.Fatalf("pending = %q, want grant", m.grants.pending)
	}
	if out := m.renderGrantsView(); !strings.Contains(out, "reads every private repo") {
		t.Errorf("the confirmation must state the cost:\n%s", out)
	}

	// Any other key cancels.
	next, cmd = m.grantsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(tuiModel)
	if cmd != nil {
		t.Error("a non-confirming key must not run the action")
	}
	if m.grants.pending != "" {
		t.Error("the pending action should be cleared")
	}
	if m.grants.notice != "cancelled" {
		t.Errorf("notice = %q, want cancelled", m.grants.notice)
	}
}

// y confirms and dispatches.
func TestGrantsViewConfirmDispatches(t *testing.T) {
	m := ghModel(api.HostGitHubCredentialView{Eligible: true})
	next, _ := m.grantsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = next.(tuiModel)
	_, cmd := m.grantsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("y must dispatch the grant")
	}
}

// G on a tenant island must not arm anything — the daemon refuses that grant,
// so arming it would promise something that cannot happen.
func TestGrantsViewGrantNotOfferedForTenant(t *testing.T) {
	m := ghModel(api.HostGitHubCredentialView{Eligible: false})
	next, cmd := m.grantsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	m = next.(tuiModel)
	if cmd != nil {
		t.Error("G must not act on a tenant island")
	}
	if m.grants.pending != "" {
		t.Error("G must not arm a confirmation on a tenant island")
	}
	if !strings.Contains(m.grants.notice, "not applicable") {
		t.Errorf("notice should explain why, got %q", m.grants.notice)
	}
}

// After a successful action the pane says the change isn't live until the
// container is recreated — otherwise the operator grants, sees no change in
// the island, and grants again.
func TestGrantsViewActionNoticeNamesTheRecreate(t *testing.T) {
	v := &grantsView{island: "proj"}
	v.applyHostGHAction(hostGHActionMsg{action: "grant"})
	if !strings.Contains(v.notice, "next created") {
		t.Errorf("notice should say when it takes effect, got %q", v.notice)
	}
	if !strings.Contains(v.notice, "dejima upgrade proj") {
		t.Errorf("notice should name the command that applies it, got %q", v.notice)
	}

	v = &grantsView{island: "proj"}
	v.applyHostGHAction(hostGHActionMsg{action: "grant", err: errFake{}})
	if !strings.Contains(v.notice, "failed") {
		t.Errorf("a failed action must say so, got %q", v.notice)
	}
}

type errFake struct{}

func (errFake) Error() string { return "boom" }

// Every summary in the pane must count every grant kind. The failure this
// guards is not a wrong number, it's a MISSING TERM — and a missing term reads
// as a smaller, safer island than the one in front of you.
//
// It has already happened twice with the same kind: the containment claim
// omitted the host GitHub credential, and the fix for that omitted it again
// from the tally ten lines below. Deriving both from grantKinds is what makes
// the next kind a one-line change instead of a scavenger hunt.
func TestGrantsSummariesCountEveryKind(t *testing.T) {
	// A single grant of EACH kind, so a summary that drops one is visible.
	full := &api.IslandGrantsResponse{
		Port:       []api.PortScopeView{{Name: "vault", HostPath: "/tmp/v", Mode: "ro"}},
		MCP:        []api.MCPGrantView{{Server: "fs"}},
		Capability: []api.CapabilityGrantView{{Target: "notify"}},
		HostGitHub: api.HostGitHubCredentialView{Eligible: true, Granted: true, GrantedAt: time.Now()},
	}
	if got, want := len(grantKinds(full)), 5; got != want {
		t.Fatalf("grantKinds returned %d kinds, want %d — a kind was added without extending the enumeration", got, want)
	}
	sum := 0
	for _, k := range grantKinds(full) {
		if k.label == "" {
			t.Error("every kind needs a label for the tally")
		}
		sum += k.n
	}
	if sum != 4 {
		t.Errorf("summed %d grants, want 4 (one of each held kind)", sum)
	}

	// The footer tally must name every kind, including the one that isn't a list.
	m := grantsModelWith(full)
	out := plain(m.renderGrantsView())
	for _, label := range []string{"Port", "MCP", "Links", "Capabilities", "GitHub"} {
		if !strings.Contains(out, label+" ") {
			t.Errorf("tally omits %q:\n%s", label, out)
		}
	}
}

// The GitHub credential alone must move the total off zero — the containment
// claim is derived from the same sum, so this is the property that stops the
// pane calling an account-wide grant "fully contained".
func TestGrantKindsCountsHostGitHub(t *testing.T) {
	denied := &api.IslandGrantsResponse{HostGitHub: api.HostGitHubCredentialView{Eligible: true}}
	granted := &api.IslandGrantsResponse{HostGitHub: api.HostGitHubCredentialView{Eligible: true, Granted: true}}

	sum := func(r *api.IslandGrantsResponse) int {
		n := 0
		for _, k := range grantKinds(r) {
			n += k.n
		}
		return n
	}
	if sum(denied) != 0 {
		t.Errorf("a denied credential contributes nothing, got %d", sum(denied))
	}
	if sum(granted) != 1 {
		t.Errorf("a granted credential must count toward the total, got %d", sum(granted))
	}
}
