package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// THE KEY ENUMERATION, which until now did not exist.
//
// Every binding in this TUI is a bare `case "x":` inside one of ~20 switches, so
// "is this letter taken, and is it safe?" has been a grep whose failure mode is a
// silent no. That is how `R` came to mean three things, and how the collisions
// below were shipped one at a time by people who each checked the file they were
// editing.
//
// This reads the bindings out of the SOURCE rather than out of a hand-kept list,
// because a hand-kept list is one more artifact that can drift from the thing it
// describes — and drift in a list of what is bound would be invisible, which is
// the property that makes it dangerous.

// csiDangerous is the byte set that ends or begins the escape sequences a
// terminal sends for arrow keys and friends. Binding one of these BARE means any
// terminal or transport that fails to deliver a sequence atomically turns an
// arrow key into a command.
//
//	up/down/right/left   ESC [ A / B / C / D
//	home/end             ESC [ H / F      (also ESC O H / F)
//	F1-F4                ESC O P / Q / R / S
//	shift-tab            ESC [ Z
//	numbered home/end/pg ESC [ n ~
//
// The intro bytes are as dangerous as the tails, from the other end: a stray ESC
// followed by a separately-delivered `[` fires whatever `[` is bound to.
//
// This is not a workaround for one broken terminal. It is that a single-byte
// binding on these letters has a blast radius equal to whatever the letter does,
// on any transport that can split a sequence — and the operator's did.
var csiDangerous = map[string]string{
	"A": "up arrow — ESC [ A",
	"B": "down arrow — ESC [ B",
	"C": "right arrow — ESC [ C",
	"D": "left arrow — ESC [ D",
	"H": "home — ESC [ H / ESC O H",
	"F": "end — ESC [ F / ESC O F",
	"P": "F1 — ESC O P",
	"Q": "F2 — ESC O Q",
	"R": "F3 — ESC O R",
	"S": "F4 — ESC O S",
	"Z": "shift-tab — ESC [ Z",
	"[": "CSI intro — a stray ESC then a separately-delivered [",
	"O": "SS3 intro — a stray ESC then a separately-delivered O",
	"~": "tail of the numbered forms — ESC [ 5 ~ etc",
}

// knownCSIBindings are the bindings that sit on a dangerous byte TODAY, each
// with the reason it is still there. They are survivable because escSwallow
// suppresses a dangerous byte arriving right after a stray ESC (see tui.go) —
// the guard, not the letter, is what makes them safe.
//
// A NEW ENTRY HERE IS A DECISION, NOT A FORMALITY. The list exists so that
// adding a binding on one of these letters costs a conversation instead of
// happening silently; anything not listed fails the test outright.
var knownCSIBindings = map[string]string{
	"A": "audit ledger, and esc/q/A to close it — long-standing, and [L] is the safe alias",
	"H": "server menu — long-standing",
	"P": "Port scopes — long-standing",
	"R": "refresh, and the secrets pane's restart — deep muscle memory, [ctrl+r] is the safe alias",
	"S": "global settings — has always had the safe `,` alias",
	"O": "owner lens — long-standing",
	"[": "reorder an agent up; `]` is its pair and is not a dangerous byte",
}

type keyBinding struct {
	key   string
	file  string
	line  int
	fn    string
	where string
}

// keyBindings parses every non-test file in this package and returns each string
// case-clause inside a function that takes a tea.KeyMsg — i.e. every key this
// TUI binds, wherever it binds it.
func keyBindings(t *testing.T) []keyBinding {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files to audit (err=%v) — this test would pass over anything", err)
	}
	var out []keyBinding
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Type.Params == nil {
				return true
			}
			takesKeyMsg := false
			for _, p := range fn.Type.Params.List {
				if sel, ok := p.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "KeyMsg" {
					takesKeyMsg = true
				}
			}
			if !takesKeyMsg {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				cc, ok := m.(*ast.CaseClause)
				if !ok {
					return true
				}
				for _, e := range cc.List {
					bl, ok := e.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(bl.Value)
					if err != nil {
						continue
					}
					pos := fset.Position(bl.Pos())
					out = append(out, keyBinding{
						key: v, file: f, line: pos.Line, fn: fn.Name.Name,
						where: fmt.Sprintf("%s:%d (%s)", f, pos.Line, fn.Name.Name),
					})
				}
				return true
			})
			return true
		})
	}
	return out
}

// The guard d1 asked for: a new binding may not land on a byte that ends or
// begins a terminal escape sequence without someone deciding to.
func TestNoNewBindingLandsOnAnEscapeSequenceByte(t *testing.T) {
	bindings := keyBindings(t)
	// A parser that finds nothing would pass this test over any source at all.
	if len(bindings) < 100 {
		t.Fatalf("only found %d bindings — the audit is not reading the source it thinks it is", len(bindings))
	}
	// ...and it must be finding the one we know is there.
	if !hasKey(bindings, "A") {
		t.Fatal(`the audit did not find the "A" binding, which exists — it is not reading key handlers`)
	}

	for _, b := range bindings {
		why, dangerous := csiDangerous[b.key]
		if !dangerous {
			continue
		}
		if _, known := knownCSIBindings[b.key]; known {
			continue
		}
		t.Errorf("%s binds %q, which is %s.\n"+
			"A terminal that splits escape sequences will fire this from an arrow key.\n"+
			"Pick another key, or add it to knownCSIBindings with the reason — deliberately.",
			b.where, b.key, why)
	}
}

// The known-collision list must not outlive the collisions. A stale entry
// excuses nothing while quietly widening what the test above will accept — the
// same failure as an exemption naming a field that no longer exists.
func TestKnownCSIBindingsAllStillExist(t *testing.T) {
	bindings := keyBindings(t)
	for key, why := range knownCSIBindings {
		if !hasKey(bindings, key) {
			t.Errorf("knownCSIBindings excuses %q (%s), which nothing binds any more — "+
				"remove it, or the next binding on that key gets waved through", key, why)
		}
		if _, dangerous := csiDangerous[key]; !dangerous {
			t.Errorf("knownCSIBindings lists %q, which is not a dangerous byte — the exemption means nothing", key)
		}
	}
}

func hasKey(bs []keyBinding, key string) bool {
	for _, b := range bs {
		if b.key == key {
			return true
		}
	}
	return false
}

// Esc has to have a reachable alternative, because on a terminal that splits
// sequences a bare Esc is indistinguishable from the start of one until the next
// byte arrives — so it can be swallowed or delayed. ctrl+[ transmits the same
// byte with no ambiguity: nothing follows it.
//
// Asserted PER FUNCTION rather than in total: a count would be satisfied by
// thirteen alternatives clustered in the creator while every other pane has
// none, which is exactly the state this found.
func TestEveryEscHandlerAcceptsCtrlBracketToo(t *testing.T) {
	bindings := keyBindings(t)
	esc, alt := map[string]string{}, map[string]bool{}
	for _, b := range bindings {
		switch b.key {
		case "esc":
			if _, seen := esc[b.where]; !seen {
				esc[b.where] = b.where
			}
		case "ctrl+[":
			alt[b.fn] = true
		}
	}
	var missing []string
	for _, b := range bindings {
		if b.key != "esc" || alt[b.fn] {
			continue
		}
		missing = append(missing, b.where)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d esc handlers have no ctrl+[ alternative. On a terminal that splits\n"+
			"escape sequences a bare esc may never arrive, and these panes become\n"+
			"unclosable:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}
