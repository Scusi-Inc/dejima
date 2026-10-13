package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"
)

// A foreground-only style is painted onto the TERMINAL's background, which we
// do not control, so its color must adapt. A style that sets its own
// background carries its own contrast and may use fixed colors.
//
// The palette was hardcoded for a dark terminal and the whole TUI was
// near-invisible on a light one — #94a3b8 on white is ~2.3:1 against WCAG AA's
// 4.5:1, and the "accent" and "title" colors were white on white. The worst
// instance was the destructive-confirm dialog, where the line telling you what
// to type to confirm discarding uncommitted work was the least readable text
// on the screen.
//
// Parsed from the AST, not grepped: the printed expression excludes comments,
// so prose mentioning lipgloss.Color cannot satisfy or trip this gate, and a
// new style is covered the moment it is declared without touching this test.
func TestPaletteForegroundsAreAdaptive(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "tui.go", nil, 0)
	if err != nil {
		t.Fatalf("parse tui.go: %v", err)
	}

	var checked int
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, v := range vs.Values {
				var buf bytes.Buffer
				if err := printer.Fprint(&buf, fset, v); err != nil {
					t.Fatalf("print expr: %v", err)
				}
				// Whitespace-stripped: a chained builder may wrap, putting a
				// newline between the "." and "Background(" and making a
				// self-contained style look foreground-only. styleFirstRun
				// wraps exactly that way and was misreported until this.
				expr := strings.Join(strings.Fields(buf.String()), "")
				if !strings.Contains(expr, ".Foreground(") {
					continue
				}
				checked++
				if strings.Contains(expr, ".Background(") {
					continue // sets its own ground; contrast is self-contained
				}
				if strings.Contains(expr, "lipgloss.Color(") {
					name := "?"
					if i < len(vs.Names) {
						name = vs.Names[i].Name
					}
					t.Errorf("%s sets a foreground with a fixed lipgloss.Color and no background — "+
						"it will be painted on the terminal's own background and is unreadable on "+
						"one of the two themes; use lipgloss.AdaptiveColor{Light: …, Dark: …}", name)
				}
			}
		}
	}
	if checked == 0 {
		// Without this the test passes vacuously if the palette moves to
		// another file — which is exactly when it stops protecting anything.
		t.Fatal("no foreground styles found in tui.go — the gate has lost its subject")
	}
}
