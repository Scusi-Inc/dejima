package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// Anything materialized from a store into an island's mount needs a way to be
// re-materialized. Four separate bugs in one week were this one shape:
//
//	grants pane      reported an island contained after a revoke it never saw
//	secrets          a `secret set` rewrote a file the container could not see
//	gh credential    a rotated token never reached any existing island
//	git author       refreshed the credential and not the identity beside it
//
// Every time, the STORE was correct, the ISLAND disagreed, and every surface
// reported the store. The operator refreshed an expired token host-side, watched
// `dejima github ls` show the new one, and several islands kept failing with
// "Bad credentials" against a file untouched for a month. Nothing connected them.
//
// credentialBindMounts runs at CONTAINER CREATE and nowhere else, so a
// materializer reachable only from there can never be updated in place. This
// finds them: a function called by credentialBindMounts that WRITES A FILE must
// also be called from somewhere else — the refresh path.
//
// It cannot check that the refresh fires at the right moment. It catches the
// specific failure of a materializer with no way to run again, which is what all
// four of those bugs were.
func TestEveryMaterializerHasARefreshPath(t *testing.T) {
	// Walk the package's own .go files rather than parser.ParseDir (deprecated,
	// and it would pull in x/tools for a job this size). Test files are excluded
	// deliberately: a materializer called only from a TEST has no refresh path.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	// funcBody[name] = the declaration, so we can ask what each one calls/writes.
	funcBody := map[string]*ast.FuncDecl{}
	parsed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed++
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				funcBody[fd.Name.Name] = fd
			}
		}
	}
	if parsed == 0 {
		t.Fatal("parsed no source files — this guard is reading the wrong directory " +
			"and would pass no matter what the package contains")
	}
	bindMounts, ok := funcBody["credentialBindMounts"]
	if !ok {
		t.Fatal("credentialBindMounts not found — it was renamed, and this guard is " +
			"now checking nothing while passing")
	}

	// calls returns every function name invoked inside a declaration.
	calls := func(fd *ast.FuncDecl) map[string]bool {
		out := map[string]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := ce.Fun.(type) {
			case *ast.Ident:
				out[fn.Name] = true
			case *ast.SelectorExpr: // s.method(...) and pkg.Func(...)
				out[fn.Sel.Name] = true
			}
			return true
		})
		return out
	}

	// A materializer writes a file. That is what makes it stale-able.
	writesAFile := func(fd *ast.FuncDecl) bool {
		c := calls(fd)
		return c["WriteFile"] || c["Rename"] || c["Create"]
	}

	var materializers []string
	for name := range calls(bindMounts) {
		fd, known := funcBody[name]
		if !known || !writesAFile(fd) {
			continue
		}
		materializers = append(materializers, name)
	}
	sort.Strings(materializers)

	if len(materializers) == 0 {
		t.Fatal("found no materializers at all — credentialBindMounts no longer calls " +
			"anything that writes a file, or the AST walk is broken. Either way this " +
			"guard is not watching what it claims to.")
	}

	for _, m := range materializers {
		callers := 0
		for name, fd := range funcBody {
			if name == m {
				continue // a self-recursive call is not a refresh path
			}
			if calls(fd)[m] {
				callers++
			}
		}
		// credentialBindMounts itself is one caller. Anything else is the refresh.
		if callers < 2 {
			t.Errorf("%s() materializes state into an island's mount and is reachable "+
				"ONLY from credentialBindMounts, which runs at container create.\n"+
				"So once an island exists, nothing can update what %s wrote: the store "+
				"changes, the island keeps the old value, and every surface reports the "+
				"store. That is the shape of four separate bugs this week.\n"+
				"Give it a refresh path (see refreshIslandSecrets, "+
				"refreshIslandGitHubConfigs) and call it wherever the source of truth "+
				"changes.", m, m)
		}
	}
	t.Logf("checked %d materializer(s): %s", len(materializers), strings.Join(materializers, ", "))
}
