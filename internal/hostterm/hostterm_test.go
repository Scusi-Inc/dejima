package hostterm

import (
	"testing"
	"time"
)

func TestNextIDMonotonic(t *testing.T) {
	s := &Store{}
	if got := s.NextID(); got != "t1" {
		t.Fatalf("empty NextID = %q, want t1", got)
	}
	s.Add(Terminal{ID: "t1"})
	s.Add(Terminal{ID: "t3"})
	s.Add(Terminal{ID: "bogus"}) // ignored
	if got := s.NextID(); got != "t4" {
		t.Errorf("NextID = %q, want t4 (max+1, never reuse the gap)", got)
	}
}

func TestTmuxName(t *testing.T) {
	if got := (Terminal{ID: "t2"}).Tmux(); got != "dejima-term-t2" {
		t.Errorf("Tmux() = %q, want dejima-term-t2", got)
	}
}

func TestAddRemoveGetRelabel(t *testing.T) {
	s := &Store{}
	s.Add(Terminal{ID: "t1", Label: "repair", CreatedAt: time.Unix(0, 0)})
	s.Add(Terminal{ID: "t2"})

	if tm, ok := s.Get("t1"); !ok || tm.Label != "repair" {
		t.Fatalf("Get(t1) = %+v, %v", tm, ok)
	}
	if !s.Relabel("t2", "  logs ") {
		t.Fatal("Relabel(t2) returned false")
	}
	if tm, _ := s.Get("t2"); tm.Label != "logs" {
		t.Errorf("relabel should trim; got %q", tm.Label)
	}
	if !s.Remove("t1") || s.Remove("t1") {
		t.Error("Remove should succeed once, then fail")
	}
	if _, ok := s.Get("t1"); ok {
		t.Error("t1 still present after Remove")
	}
	if len(s.Terminals) != 1 {
		t.Errorf("want 1 terminal left, got %d", len(s.Terminals))
	}
}
