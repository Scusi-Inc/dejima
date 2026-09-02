// Package srcscan prepares source text for guards that search it.
//
// A source-scanning guard asserts that some token is present or absent in a
// file. Every one of them has the same hole: a comment EXPLAINING the token
// contains the token. Delete the real thing, leave the prose describing it, and
// the guard still passes — on the sentence about the code rather than the code.
//
// That has now happened four times in one week, across DEJIMA_ROLE (three
// times) and a checksum step (once), the last written by someone who had read
// the write-up of the first three that same afternoon. Documenting it did not
// prevent it, which is the useful finding: the intervention has to be
// mechanical. Strip the comments once, here, and every guard that uses this
// stops being able to match prose.
//
// # The stripping errs toward removing too little, deliberately
//
// The two mistakes are not symmetric. Strip too little and a guard matches a
// comment: a false positive, noisy, immediately obvious to whoever is looking
// at it. Strip too much and a guard stops seeing real code: it passes, silently,
// for the same reason the code is broken — which is the exact failure this
// package exists to end, reintroduced one layer down.
//
// So StripLineComments removes only whole-line comments, never trailing ones,
// because deciding whether a mid-line marker is a comment or a literal needs a
// parser for the language in question. A trailing comment left in can only cost
// a false positive. Guessing wrong about a quoted '#' could cost a real match.
package srcscan

import (
	"go/scanner"
	"go/token"
	"strings"
)

// StripGoComments blanks every comment in Go source, preserving the length and
// line structure of the input so that offsets, line numbers and counts of
// anything else in the file are unchanged.
//
// It is exact rather than heuristic: the Go scanner decides what a comment is,
// so a "//" inside a string literal survives and a comment containing code does
// not. On a lexical error the input is returned unchanged, with ok false — a
// guard should scan the raw file rather than a partially processed one, since a
// half-stripped file is the one state that could hide a real match.
func StripGoComments(src string) (out string, ok bool) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))

	var s scanner.Scanner
	var lexErr bool
	s.Init(file, []byte(src), func(token.Position, string) { lexErr = true }, scanner.ScanComments)

	b := []byte(src)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		start := file.Offset(pos)
		end := start + len(lit)
		if start < 0 || end > len(b) {
			return src, false
		}
		for i := start; i < end; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	if lexErr {
		return src, false
	}
	return string(b), true
}

// StripLineComments blanks whole-line comments — lines whose first non-space
// character is marker — in text that is not Go: an embedded shell script, a
// YAML fixture, a Dockerfile. Pass "#" for those.
//
// Whole-line only. A trailing comment after code stays, and that is the
// conservative choice: see the package comment for why the two errors are not
// symmetric. Line count and line lengths are preserved so a guard reporting a
// line number still points at the right place in the original.
func StripLineComments(src, marker string) string {
	if marker == "" {
		return src
	}
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimLeft(ln, " \t"), marker) {
			lines[i] = strings.Repeat(" ", len(ln))
		}
	}
	return strings.Join(lines, "\n")
}
