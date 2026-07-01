package main

import "testing"

// The pin/unpin commands wire the idle-hibernate exemption (dejima pin / unpin).
// Also satisfies the coverage gate for the two new CLI commands.
func TestPinUnpinCommands(t *testing.T) {
	if got := newPinCmd().Name(); got != "pin" {
		t.Errorf(`pin command Name() = %q, want "pin"`, got)
	}
	if got := newUnpinCmd().Name(); got != "unpin" {
		t.Errorf(`unpin command Name() = %q, want "unpin"`, got)
	}
	// Both take exactly one island argument.
	if err := newPinCmd().Args(newPinCmd(), []string{}); err == nil {
		t.Error("dejima pin should require an island name")
	}
	if err := newUnpinCmd().Args(newUnpinCmd(), []string{"a", "b"}); err == nil {
		t.Error("dejima unpin should reject extra args")
	}
}
