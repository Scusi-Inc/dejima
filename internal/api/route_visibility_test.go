package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The route-parity gate (sdk/openapi_parity.py) finds routes by matching literal
// `mux.HandleFunc("VERB /path", …)` strings in these sources. That has one blind
// spot: a route registered through a LOOP or a variable is invisible to it —
// undocumented, AND silently exempt from the gate whose whole job is catching
// undocumented routes.
//
// It was found the only way a textual scan can be. Someone registered seven
// verbs in a loop, the gate reported ONE missing route, and they noticed the
// number was too small. That reflex only works when you already know what the
// number should be, which is not a check.
//
// So this compares what the daemon ACTUALLY registers against what a literal
// scan can see. It does not replace the Python gate — it guards the Python
// gate's blind spot, which nothing else could.
func TestEveryRouteIsVisibleToTheParityGate(t *testing.T) {
	srv := &Server{}
	registered := srv.registeredRoutes()
	if len(registered) == 0 {
		t.Fatal("no routes registered at all — this guard is reading nothing, " +
			"which is the failure it exists to catch, in itself")
	}

	// The gate globs internal/api/*.go, so read the same set.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var sources strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		sources.Write(b)
	}
	src := sources.String()
	if !strings.Contains(src, "mux.HandleFunc(") {
		t.Fatal("no literal HandleFunc calls found in the sources — the scan this " +
			"test emulates would find nothing, so agreement below would be vacuous")
	}

	var invisible []string
	for _, pattern := range registered {
		// The literal the Python gate matches on. If a route is built from a
		// variable or a loop, this exact string is not in the file.
		if !strings.Contains(src, `"`+pattern+`"`) {
			invisible = append(invisible, pattern)
		}
	}
	if len(invisible) > 0 {
		t.Errorf("these routes are registered but NOT visible as literals, so "+
			"sdk/openapi_parity.py cannot see them — they are undocumented and "+
			"silently exempt from the gate that would say so:\n  %s\n\n"+
			"Write them out longhand, or teach the gate to resolve them.",
			strings.Join(invisible, "\n  "))
	}
}
