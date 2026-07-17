package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/reposrc"
)

func TestIsGitHubIdentityGateError(t *testing.T) {
	gate := errors.New(`"https://github.com/x/p" needs a GitHub identity to clone (it isn't anonymously reachable) but none is configured — run ...`)
	if !isGitHubIdentityGateError(gate) {
		t.Error("should match the daemon's identity-gate phrase")
	}
	if isGitHubIdentityGateError(errors.New("some other create failure")) {
		t.Error("should not match unrelated errors")
	}
	if isGitHubIdentityGateError(nil) {
		t.Error("nil is not a gate error")
	}
}

// TestCreateGateRoutesToGuidedStep: a create refused by the identity gate moves
// the creator into the guided connect step instead of a dead-end error.
func TestCreateGateRoutesToGuidedStep(t *testing.T) {
	m := tuiModel{creator: &creatorModel{
		creating:   true,
		resolution: reposrc.Resolution{Repo: "https://github.com/x/private"},
	}}
	updated, _ := m.Update(islandCreatedMsg{
		err: errors.New(`"https://github.com/x/private" needs a GitHub identity to clone`),
	})
	c := updated.(tuiModel).creator
	if c == nil || c.step != stepGitHubGate {
		t.Fatalf("gate error should route to stepGitHubGate, got %+v", c)
	}
	if c.gateRepo == "" || c.err != "" {
		t.Errorf("gate should set gateRepo + clear the raw error, got repo=%q err=%q", c.gateRepo, c.err)
	}
}

// TestCreateGateNonGateErrorShowsError: an unrelated create failure keeps the
// normal error display (does NOT hijack into the guided step).
func TestCreateGateNonGateErrorShowsError(t *testing.T) {
	m := tuiModel{creator: &creatorModel{creating: true}}
	updated, _ := m.Update(islandCreatedMsg{err: errors.New("disk full")})
	c := updated.(tuiModel).creator
	if c.step == stepGitHubGate {
		t.Error("a non-gate error must not route to the guided step")
	}
	if c.err != "disk full" {
		t.Errorf("non-gate error should show verbatim, got %q", c.err)
	}
}

func TestCreateGateForceSetsOverride(t *testing.T) {
	m := tuiModel{creator: &creatorModel{
		step:   stepGitHubGate,
		agents: []api.AgentSpecRequest{{Type: "claude-code"}},
	}}
	updated, _ := m.creatorGitHubGateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	c := updated.(tuiModel).creator
	if !c.forceNoIdentity || c.step != stepCreate {
		t.Errorf("[f] should set the override + move to create, got force=%v step=%v", c.forceNoIdentity, c.step)
	}
	if !c.buildRequest().AllowNoIdentity {
		t.Error("forceNoIdentity should flow into req.AllowNoIdentity")
	}
}

func TestCreateGateEscCancels(t *testing.T) {
	m := tuiModel{creator: &creatorModel{step: stepGitHubGate}}
	updated, _ := m.creatorGitHubGateKey(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(tuiModel).creator != nil {
		t.Error("esc should cancel the creator")
	}
}

func TestCreateGateViewRenders(t *testing.T) {
	c := &creatorModel{step: stepGitHubGate, gateRepo: "github.com/x/private"}
	out := c.view(70)
	for _, want := range []string{"github.com/x/private", "Connect your GitHub", "[f]"} {
		if !strings.Contains(out, want) {
			t.Errorf("gate view missing %q\n---\n%s", want, out)
		}
	}
}
