package project

import (
	"strings"
	"testing"
)

func specs() []AgentSpec {
	return []AgentSpec{
		{ID: "a1", Label: "build"},
		{ID: "a2", Label: "Frontend"},
		{ID: "a3", Label: "build"}, // duplicate label (legacy/pre-dedup data)
		{ID: "a4"},                 // no label
	}
}

func TestResolveAgentRef_IDWins(t *testing.T) {
	// An exact id resolves to itself even though "a1" is also a label-less peer's
	// neighbor — ids must never be shadowed.
	got, err := ResolveAgentRef(specs(), "a1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "a1" {
		t.Errorf("id should resolve to itself: got %q", got)
	}
	// An unlabeled agent is still addressable by its id.
	if got, err := ResolveAgentRef(specs(), "a4"); err != nil || got != "a4" {
		t.Errorf("unlabeled id: got %q err %v", got, err)
	}
}

func TestResolveAgentRef_LabelCaseInsensitive(t *testing.T) {
	for _, ref := range []string{"frontend", "Frontend", "FRONTEND"} {
		got, err := ResolveAgentRef(specs(), ref)
		if err != nil {
			t.Fatalf("ref %q: unexpected err: %v", ref, err)
		}
		if got != "a2" {
			t.Errorf("ref %q should resolve to a2: got %q", ref, got)
		}
	}
}

func TestResolveAgentRef_Ambiguous(t *testing.T) {
	_, err := ResolveAgentRef(specs(), "build")
	if err == nil {
		t.Fatal("ambiguous label should error")
	}
	msg := err.Error()
	// Must list BOTH matches with id(label) and point at the id.
	for _, want := range []string{"ambiguous", `"build"`, "a1(build)", "a3(build)", "use the id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ambiguity error %q missing %q", msg, want)
		}
	}
}

func TestResolveAgentRef_NoMatch(t *testing.T) {
	_, err := ResolveAgentRef(specs(), "nope")
	if err == nil {
		t.Fatal("unknown ref should error")
	}
	if !strings.Contains(err.Error(), "no such agent") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveAgentRef_EmptyErrors(t *testing.T) {
	if _, err := ResolveAgentRef(specs(), "   "); err == nil {
		t.Error("empty ref should error")
	}
}

func TestProjectResolveAgentRef(t *testing.T) {
	p := &Project{Agents: specs()}
	// Label resolves to the spec.
	a, err := p.ResolveAgentRef("frontend")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if a.ID != "a2" {
		t.Errorf("expected a2, got %q", a.ID)
	}
	// Id wins.
	if a, err := p.ResolveAgentRef("a1"); err != nil || a.ID != "a1" {
		t.Errorf("id resolve: got %v err %v", a, err)
	}
	// Ambiguous propagates.
	if _, err := p.ResolveAgentRef("build"); err == nil {
		t.Error("ambiguous label should error through the method")
	}
}
