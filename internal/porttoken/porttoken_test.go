package porttoken

import (
	"testing"

	"github.com/aoos/dejima/internal/project"
)

func TestPortToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Two islands with configs so project.List sees them.
	for _, n := range []string{"alpha", "beta"} {
		p := &project.Project{Name: n, DesiredState: project.StateRunning}
		if err := p.Save(); err != nil {
			t.Fatalf("save %s: %v", n, err)
		}
	}

	ta, err := Ensure("alpha")
	if err != nil || ta == "" {
		t.Fatalf("ensure alpha: %v tok=%q", err, ta)
	}
	if again, _ := Ensure("alpha"); again != ta {
		t.Errorf("Ensure not idempotent: %q != %q", again, ta)
	}
	tb, _ := Ensure("beta")
	if tb == ta {
		t.Error("tokens not unique across islands")
	}

	if isl, ok := IslandForToken(ta); !ok || isl != "alpha" {
		t.Errorf("IslandForToken(alpha) = %q,%v want alpha,true", isl, ok)
	}
	if isl, ok := IslandForToken(tb); !ok || isl != "beta" {
		t.Errorf("IslandForToken(beta) = %q,%v want beta,true", isl, ok)
	}
	if isl, ok := IslandForToken("deadbeefnotatoken"); ok {
		t.Errorf("unknown token resolved to %q", isl)
	}
	if _, ok := IslandForToken(""); ok {
		t.Error("empty token resolved")
	}
}
