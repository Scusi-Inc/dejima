package project

import "testing"

func TestCapabilityGrants(t *testing.T) {
	p := &Project{Name: "oc"}
	if _, ok := p.CapabilityGrantByTarget("MorningBrief"); ok {
		t.Fatal("expected no grant initially")
	}
	if g, err := p.AddCapabilityGrant(CapabilityGrant{Target: "MorningBrief"}); err != nil || g.Target != "MorningBrief" {
		t.Fatalf("add: err=%v grant=%+v", err, g)
	}
	if _, ok := p.CapabilityGrantByTarget("MorningBrief"); !ok {
		t.Fatal("expected grant after add")
	}
	if _, err := p.AddCapabilityGrant(CapabilityGrant{Target: "MorningBrief"}); err == nil {
		t.Fatal("expected duplicate target to be rejected")
	}
	if len(p.Capabilities) != 1 {
		t.Fatalf("want 1 grant, got %d", len(p.Capabilities))
	}
	if removed, ok := p.RemoveCapabilityGrant("MorningBrief"); !ok || removed.Target != "MorningBrief" {
		t.Fatalf("remove: ok=%v removed=%+v", ok, removed)
	}
	if len(p.Capabilities) != 0 {
		t.Fatalf("want 0 grants after remove, got %d", len(p.Capabilities))
	}
	if _, ok := p.RemoveCapabilityGrant("MorningBrief"); ok {
		t.Fatal("expected remove of absent grant to report ok=false")
	}
}

func TestValidateCapabilityTarget(t *testing.T) {
	valid := []string{"MorningBrief", "Morning Brief", "send-text", "note.append", "a"}
	for _, s := range valid {
		if err := ValidateCapabilityTarget(s); err != nil {
			t.Errorf("want valid, %q rejected: %v", s, err)
		}
	}
	// empty, leading/trailing space, path separators, traversal, control char.
	invalid := []string{"", " lead", "trail ", "a/b", `a\b`, "..", "a..b", "a\x00b"}
	for _, s := range invalid {
		if err := ValidateCapabilityTarget(s); err == nil {
			t.Errorf("want invalid, %q accepted", s)
		}
	}
}
