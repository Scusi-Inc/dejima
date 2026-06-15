package events

import "testing"

func TestKnownType(t *testing.T) {
	// The observability event types must be subscribable.
	for _, ok := range []Type{TypeContainerCrashed, TypeDaemonStarted, TypePanicEngaged, TypePanicCleared, TypeIslandCreated} {
		if !KnownType(ok) {
			t.Errorf("KnownType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []Type{"container.crahsed", "", "nope"} {
		if KnownType(bad) {
			t.Errorf("KnownType(%q) = true, want false", bad)
		}
	}
}

func TestKnownTypesIsCopy(t *testing.T) {
	a := KnownTypes()
	if len(a) == 0 {
		t.Fatal("catalog is empty")
	}
	a[0] = "mutated"
	if KnownTypes()[0] == "mutated" {
		t.Error("KnownTypes returned a slice aliasing the package catalog")
	}
}
