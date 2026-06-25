package policy

import (
	"testing"
	"time"

	"github.com/aoos/dejima/internal/link"
)

func req(action string, tier link.Tier) link.ActionRequest {
	return link.ActionRequest{From: "a", To: "b", Topic: "t", Action: action, Tier: tier}
}

func addRule(t *testing.T, action string, max int, ttl time.Duration) {
	t.Helper()
	if _, err := Update(func(s *Store) error {
		s.Add(Rule{From: "a", To: "b", Topic: "t", Action: action, MaxCount: max}, ttl)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A matching rule auto-approves up to its budget, then stops (falls back to the
// human queue).
func TestConsume_BudgetExhausts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	addRule(t, "dispatch", 2, time.Hour)

	for i := 1; i <= 2; i++ {
		if _, ok, err := Consume(req("dispatch", link.TierMutating)); err != nil || !ok {
			t.Fatalf("consume %d: ok=%v err=%v (want auto-approve within budget)", i, ok, err)
		}
	}
	if _, ok, _ := Consume(req("dispatch", link.TierMutating)); ok {
		t.Error("3rd consume should NOT auto-approve (budget of 2 exhausted)")
	}
}

// Destructive is never auto-approved, even with a matching rule.
func TestConsume_DestructiveNeverAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	addRule(t, "delete", 5, time.Hour)
	if _, ok, _ := Consume(req("delete", link.TierDestructive)); ok {
		t.Fatal("destructive action must never be auto-approved by a rule")
	}
}

// No rule for the action → no auto-approve (prompt-everything default).
func TestConsume_NoRuleNoAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	addRule(t, "dispatch", 5, time.Hour)
	if _, ok, _ := Consume(req("build", link.TierMutating)); ok {
		t.Error("an action with no matching rule must not auto-approve")
	}
}

// An expired rule doesn't match (and gets pruned).
func TestConsume_ExpiredDoesNotMatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	addRule(t, "dispatch", 5, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok, _ := Consume(req("dispatch", link.TierMutating)); ok {
		t.Error("an expired rule must not auto-approve")
	}
	s, _ := Load()
	if len(s.Rules) != 0 {
		t.Errorf("expired rule should have been pruned, store has %d", len(s.Rules))
	}
}
