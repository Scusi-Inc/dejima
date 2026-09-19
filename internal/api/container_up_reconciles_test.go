package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EVERY PATH THAT BRINGS A CONTAINER UP MUST RECONCILE THE NON-PRIMARY AGENTS.
//
// The entrypoint launches agent 0 and nothing else, so any path that creates or
// starts a container and returns without calling reconcileAgents leaves every
// co-located agent with no tmux session — silently, and usually with a 200.
//
// This has now happened five times in four places:
//
//	14a7ac1  AdoptExisting, restartToRunning, startIslandIfStopped
//	#445     resetIsland
//	this PR  wakeIslandFor
//
// The first four were found by enumerating CALLERS OF reconcileAgents and
// checking the value each passed. That search cannot see a function which never
// calls it at all — the bug in its most complete form is invisible to a search
// shaped around the milder one. wakeIslandFor sat in that blind spot for the
// whole series, while schedule.go's "this mirrors wakeIslandFor's core" pointed
// straight at it from directly above the copy that did get fixed.
//
// So this enumerates from the other end: every function that brings a container
// up, asked whether it reconciles. A lesson that recurs twice becomes a check,
// not a third comment.
func TestEveryContainerUpPathReconciles(t *testing.T) {
	// Functions allowed to bring a container up WITHOUT reconciling, each with
	// the reason. Empty today, and that is the point: adding a name here is a
	// deliberate, reviewed act rather than an omission nobody notices.
	allowed := map[string]string{}

	const (
		bringsUpA = "createContainerForProject"
		bringsUpB = "StartContainer"
	)
	reconcilers := map[string]bool{"reconcileAgentsAsync": true, "reconcileAgents": true}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders, found []string

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == bringsUpA {
				continue // the creator itself is the primitive, not a path
			}
			var bringsUp, reconciles bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch {
				case sel.Sel.Name == bringsUpA, sel.Sel.Name == bringsUpB:
					bringsUp = true
				case reconcilers[sel.Sel.Name]:
					reconciles = true
				}
				return true
			})
			if !bringsUp {
				continue
			}
			where := fset.Position(fn.Pos())
			found = append(found, fn.Name.Name)
			if reconciles {
				continue
			}
			if _, ok := allowed[fn.Name.Name]; ok {
				continue
			}
			offenders = append(offenders, fn.Name.Name+" ("+filepath.Base(where.Filename)+":"+strconv.Itoa(where.Line)+")")
		}
	}

	// THE CONTROL. A scan that matches nothing passes for the same reason a
	// perfect codebase does, and this one walks files by glob and matches by
	// AST shape — a rename, a move, or a build tag would empty it silently.
	// Five is the number of paths that existed when this was written; fewer
	// means the scan stopped seeing the population, not that the population
	// improved.
	if len(found) < 5 {
		t.Fatalf("this check found only %d container-up paths (%v). It is no longer looking at the code it was written to guard — fix the scan before trusting a pass", len(found), found)
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("these bring a container up and never reconcile, so the entrypoint's PRIMARY agent comes back alone:\n  %s\n\n"+
			"Add `s.reconcileAgentsAsync(p, s.containerResumesPrimary(ctx, p))` — or, if the path genuinely must not, "+
			"name it in this test's `allowed` map with the reason.", strings.Join(offenders, "\n  "))
	}
}
