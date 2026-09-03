package main

// Coverage gate — the FRESHNESS mechanism for the Dejima test suite.
//
// This test enumerates the project's two machine-enumerable surfaces of truth:
//
//   1. every cobra command in newRootCmd() (the CLI tree), and
//   2. every operation in openapi.yaml (the API, 88 ops),
//
// and asserts that each one is REFERENCED by at least one test (a Go *_test.go
// file or one of the live shell suites under scripts/). A new route or command
// that lands with no test fails CI here — that is the guarantee that the suite
// cannot silently drift behind the product.
//
// Because not every command/op is covered the day this gate lands, the gate is a
// RATCHET: known-uncovered surface is listed in testdata/coverage_waivers.txt.
// The gate fails when (a) NEW surface appears that is neither tested nor waived,
// or (b) a waived entry has since gained a test (a STALE waiver — it must be
// removed so the ratchet only ever tightens). You can add a test and delete the
// waiver line; you cannot add a feature and skip both without an explicit,
// reviewed edit to the waiver file. See docs/testing/coverage-gate.md.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/srcscan"
)

// repoRoot walks up from the test's working directory to the dir holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod above %s", dir)
		}
		dir = parent
	}
}

// corpusFile is one searched file, kept whole rather than concatenated so a
// match can say WHERE it came from and so Go and shell can be matched by
// different rules. See cliReferenced for why the rules differ.
type corpusFile struct {
	rel   string // path relative to the repo root
	goSrc bool   // Go test file (vs a shell suite under scripts/)
	text  string // comments already blanked
}

// testCorpus is every Go test file plus the live shell suites under scripts/ —
// the body of text the gate searches for references. Worktrees and per-agent
// checkouts under .claude/ and .agents/ are excluded so a sibling branch can
// never spoof coverage on this one.
func testCorpus(t *testing.T, root string) []corpusFile {
	t.Helper()
	var files []corpusFile
	// Skip nested worktrees / per-agent checkouts / vendored deps so a sibling
	// branch can't spoof coverage here. Match on the path RELATIVE to root: when
	// the gate itself runs inside a worktree the absolute root contains ".claude",
	// which must not cause the whole tree to be skipped.
	skipDir := func(rel string) bool {
		return strings.HasPrefix(rel, ".claude/worktrees") ||
			strings.HasPrefix(rel, ".agents/") ||
			rel == ".agents" ||
			rel == "node_modules" ||
			strings.Contains(rel, "/node_modules")
	}
	// This very file would otherwise "reference" everything it enumerates; omit it.
	const selfBase = "coverage_gate_test.go"
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if info.IsDir() {
			if skipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(p)
		if base == selfBase {
			return nil
		}
		isGoTest := strings.HasSuffix(base, "_test.go")
		isShell := strings.HasSuffix(base, ".sh") && strings.Contains(p, "/scripts/")
		if !isGoTest && !isShell {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		text, serr := corpusText(rel, string(data), isGoTest)
		if serr != nil {
			return serr
		}
		files = append(files, corpusFile{rel: rel, goSrc: isGoTest, text: text})
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	// The control: an empty or tiny corpus reports every command uncovered and
	// every waiver orphaned, which is loud — but a corpus missing just the SHELL
	// half would quietly under-credit, so assert both kinds are present.
	var goN, shN int
	for _, f := range files {
		if f.goSrc {
			goN++
		} else {
			shN++
		}
	}
	if goN < 20 || shN < 3 {
		t.Fatalf("corpus looks broken: %d Go test files, %d shell suites — "+
			"the gate's answers are meaningless until the walk is fixed", goN, shN)
	}
	return files
}

// match reports the first file and line where re matches, "" if nowhere. Only
// files satisfying want are searched, so a rule can apply to Go or shell alone.
func match(files []corpusFile, re *regexp.Regexp, want func(corpusFile) bool) string {
	for _, f := range files {
		if want != nil && !want(f) {
			continue
		}
		if loc := re.FindStringIndex(f.text); loc != nil {
			line := 1 + strings.Count(f.text[:loc[0]], "\n")
			return fmt.Sprintf("%s:%d", f.rel, line)
		}
	}
	return ""
}

// corpusText prepares one file for the corpus by blanking its comments.
//
// COMMENTS ARE NOT COVERAGE, and crediting them is not merely noisy here. d1
// hit this writing 96f6a54: a comment explaining why operators should NOT be
// sent to `dejima auth push` matched, the gate reported that command's waiver
// as STALE, and the ratchet's remedy for a stale waiver is to delete it — for a
// command that still has no test. A false positive that TIGHTENS a ratchet is
// worse than one that nags, because acting on it removes real protection.
//
// Prose arguing AGAINST a command credited it. That is the shape, and it is why
// the remedy is mechanical (internal/srcscan) rather than a rule about how to
// write comments: this file already documented the hazard.
//
// A file that cannot be stripped is an ERROR, never a fall-back to the raw
// text. Falling back would restore the bug in exactly the case nobody is
// watching, and silently.
func corpusText(rel, src string, isGoTest bool) (string, error) {
	if isGoTest {
		stripped, ok := srcscan.StripGoComments(src)
		if !ok {
			return "", fmt.Errorf("could not strip comments from %s — "+
				"scanning it raw would credit prose about a command as a test of it", rel)
		}
		return stripped, nil
	}
	// Shell. Whole-line comments only; see internal/srcscan for why guessing at
	// a trailing '#' would be the more dangerous error.
	return srcscan.StripLineComments(src, "#"), nil
}

// --- CLI surface -----------------------------------------------------------

// cliCommands returns every runnable command path (minus the "dejima " prefix),
// e.g. "agent ls". The empty string (the root command) is excluded.
func cliCommands() []string {
	var out []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() {
			path := strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), "dejima"))
			if path != "" {
				out = append(out, path)
			}
		}
		for _, ch := range c.Commands() {
			walk(ch)
		}
	}
	walk(newRootCmd())
	sort.Strings(out)
	return out
}

// cliReferenced reports where a command is INVOKED in the corpus, and "" when
// it is only mentioned. The two forms are matched in the file kind where each
// one means invocation, and nowhere else:
//
//   - Go test:  a quoted-arg sequence, `"agent", "ls"` — what SetArgs and the
//     runCLI helpers are built from.
//   - Shell:    `dejima agent ls` — in a shell suite that IS the invocation.
//
// The human spelling used to count in Go too, which is issue #335: an
// expected-output assertion quoting another command's remedy —
//
//	if !strings.Contains(hint, "dejima ssh enroll") { … }
//
// — marked `cli ssh enroll` a STALE waiver, and the cure for a stale waiver is
// to delete it. Green became reachable one sed away from a claim of coverage
// that did not exist, and error messages naming a remedy are good practice, so
// the gate was penalising the pattern it should reward.
//
// This is the same defect as the comment case (see corpusText) one level in:
// there, prose ABOUT a command credited it; here, a string quoting a command
// credited it. Both fail toward "you have more coverage than you think", which
// is the direction nobody checks.
func cliReferenced(files []corpusFile, cmd string) string {
	toks := strings.Fields(cmd)
	quoted := make([]string, len(toks))
	esc := make([]string, len(toks))
	for i, tk := range toks {
		quoted[i] = regexp.QuoteMeta(`"` + tk + `"`)
		esc[i] = regexp.QuoteMeta(tk)
	}
	goForm := regexp.MustCompile(strings.Join(quoted, `\s*,\s*`))
	if where := match(files, goForm, func(f corpusFile) bool { return f.goSrc }); where != "" {
		return where
	}
	shForm := regexp.MustCompile(`dejima\s+` + strings.Join(esc, `\s+`))
	return match(files, shForm, func(f corpusFile) bool { return !f.goSrc })
}

// --- API surface -----------------------------------------------------------

type apiOp struct {
	id, method, path string
}

// apiOps parses openapi.yaml with a small indentation-aware scanner (paths at 2
// spaces, methods at 4, operationId at 6) — regular enough that this avoids
// pulling a YAML dependency into the build for a test-only gate. The Python
// route-parity check (sdk/openapi_parity.py) owns the full structural parse.
func apiOps(t *testing.T, root string) []apiOp {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "openapi.yaml"))
	if err != nil {
		t.Fatalf("open openapi.yaml: %v", err)
	}
	defer f.Close()

	methodRe := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	pathRe := regexp.MustCompile(`^  (/\S+):\s*$`)
	opIDRe := regexp.MustCompile(`^      operationId:\s*(\S+)\s*$`)

	var ops []apiOp
	var curPath, curMethod string
	inPaths := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "paths:" {
			inPaths = true
			continue
		}
		if !inPaths {
			continue
		}
		// A non-indented, non-blank line ends the paths: block.
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if m := pathRe.FindStringSubmatch(line); m != nil {
			curPath, curMethod = m[1], ""
			continue
		}
		if m := methodRe.FindStringSubmatch(line); m != nil {
			curMethod = strings.ToUpper(m[1])
			continue
		}
		if m := opIDRe.FindStringSubmatch(line); m != nil && curPath != "" && curMethod != "" {
			ops = append(ops, apiOp{id: m[1], method: curMethod, path: curPath})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan openapi.yaml: %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("parsed zero operations from openapi.yaml — the gate's enumerator is broken")
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].path != ops[j].path {
			return ops[i].path < ops[j].path
		}
		return ops[i].method < ops[j].method
	})
	return ops
}

// apiReferenced credits an operation when its operationId literal OR a literal
// matching its path (with each {param} standing in for one path segment)
// appears in the corpus, and reports where.
//
// Unlike the CLI side, a path inside a string literal IS how a Go test reaches
// a route — `client.Get(srv.URL + "/v1/islands/" + name + "/hibernate")` — so
// there is no equivalent of the #335 rule to apply here. Comments are still
// stripped, which is what stops prose about a route from counting.
func apiReferenced(files []corpusFile, op apiOp) string {
	if op.id != "" {
		if where := match(files, regexp.MustCompile(regexp.QuoteMeta(op.id)), nil); where != "" {
			return where
		}
	}
	segs := strings.Split(op.path, "/")
	parts := make([]string, len(segs))
	for i, s := range segs {
		if strings.HasPrefix(s, "{") {
			parts[i] = `[^/"' ]+`
		} else {
			parts[i] = regexp.QuoteMeta(s)
		}
	}
	return match(files, regexp.MustCompile(strings.Join(parts, "/")), nil)
}

// --- waivers ---------------------------------------------------------------

// loadWaivers reads testdata/coverage_waivers.txt: one entry per line; blank
// lines, full-line #-comments, and trailing inline #-comments are ignored. CLI
// entries are "cli <command path>"; API entries are "api METHOD /path".
func loadWaivers(t *testing.T, root string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "cmd", "dejima", "testdata", "coverage_waivers.txt"))
	if err != nil {
		t.Fatalf("read waivers: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		// Strip a trailing inline comment ("cli tui   # interactive") so entries
		// can be documented in place, then trim.
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out[line] = true
	}
	return out
}

// TestCoverageGate is the freshness gate. It fails when surface is uncovered and
// unwaived (a new untested feature), when a waiver is stale (now covered — must
// be removed), or when a waiver matches nothing real (typo/removed surface).
// Together these make the waiver list a one-way ratchet that only tightens.
func TestCoverageGate(t *testing.T) {
	root := repoRoot(t)
	corpus := testCorpus(t, root)
	waivers := loadWaivers(t, root)
	usedWaiver := map[string]bool{}

	var uncovered, staleWaived []string

	// where is "" when nothing referenced the surface, else file:line. Reporting
	// the location is issue #335's other half: a stale-waiver report is a demand
	// to delete a waiver, and the person acting on it could not previously tell a
	// real test from a quoted string without grepping for it themselves.
	check := func(key, where, uncoveredMsg string) {
		if waivers[key] {
			usedWaiver[key] = true
			if where != "" {
				staleWaived = append(staleWaived,
					key+" (now invoked at "+where+" — delete the waiver)")
			}
			return
		}
		if where == "" {
			uncovered = append(uncovered, uncoveredMsg)
		}
	}

	for _, cmd := range cliCommands() {
		check("cli "+cmd, cliReferenced(corpus, cmd),
			"cli "+cmd+"  (cobra command with no test that invokes it)")
	}
	for _, op := range apiOps(t, root) {
		key := "api " + op.method + " " + op.path
		check(key, apiReferenced(corpus, op),
			key+"  (openapi op "+op.id+" with no referencing test)")
	}

	var orphanWaivers []string
	for w := range waivers {
		if !usedWaiver[w] {
			orphanWaivers = append(orphanWaivers, w)
		}
	}

	sort.Strings(uncovered)
	sort.Strings(staleWaived)
	sort.Strings(orphanWaivers)

	if len(uncovered) > 0 {
		t.Errorf("NEW UNTESTED SURFACE (%d) — add a test, or (only if deliberate) waive it in "+
			"cmd/dejima/testdata/coverage_waivers.txt:\n  %s",
			len(uncovered), strings.Join(uncovered, "\n  "))
	}
	if len(staleWaived) > 0 {
		t.Errorf("STALE WAIVERS (%d) — these are now tested; delete their lines from the waiver "+
			"file so the ratchet tightens:\n  %s",
			len(staleWaived), strings.Join(staleWaived, "\n  "))
	}
	if len(orphanWaivers) > 0 {
		t.Errorf("ORPHAN WAIVERS (%d) — these match no current command/op (typo or removed "+
			"surface); delete them:\n  %s",
			len(orphanWaivers), strings.Join(orphanWaivers, "\n  "))
	}
}

// goFile / shFile build one-file corpora for the tests below, through the real
// corpusText so the stripping under test is the stripping that ships.
func goFile(t *testing.T, src string) []corpusFile {
	t.Helper()
	text, err := corpusText("x_test.go", src, true)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	return []corpusFile{{rel: "x_test.go", goSrc: true, text: text}}
}

func shFile(t *testing.T, src string) []corpusFile {
	t.Helper()
	text, err := corpusText("scripts/x.sh", src, false)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	return []corpusFile{{rel: "scripts/x.sh", goSrc: false, text: text}}
}

// TestMentionIsNotCoverage is the control for what the gate accepts as evidence
// that a command is tested. A command must be INVOKED; naming it is not enough.
//
// Two ways of naming one have each shipped a false positive, and both failed in
// the same direction — "you have more coverage than you think", which nothing
// else in the suite would contradict:
//
//   - A COMMENT about a command credited it. d1 hit this on 96f6a54 writing
//     prose about why operators should NOT be sent to `dejima auth push`.
//   - A STRING quoting a command credited it (issue #335): an expected-output
//     assertion checking that an error names the right remedy.
//
// Both then reported the command's waiver STALE, and the cure for a stale
// waiver is to delete it — so acting on either report claims coverage that does
// not exist, permanently. That is why these assertions are worth their length.
func TestMentionIsNotCoverage(t *testing.T) {
	// #335, verbatim from the issue: the remedy for one command, asserted in the
	// expected output of another.
	quoting := "package main\n" +
		"func TestOpenFails(t *testing.T) {\n" +
		"\tif !strings.Contains(hint, \"dejima ssh enroll\") { t.Error(\"no remedy\") }\n" +
		"}\n"
	if where := cliReferenced(goFile(t, quoting), "ssh enroll"); where != "" {
		t.Errorf("a command quoted in an expected-output assertion counts as invoked (matched %s)", where)
	}

	// A comment naming one — the earlier half of the same bug.
	commented := "package main\n" +
		"// Do not send operators to dejima auth push — it is not the path.\n" +
		"func TestSomething(t *testing.T) {}\n"
	if where := cliReferenced(goFile(t, commented), "auth push"); where != "" {
		t.Errorf("a command named only in a comment counts as invoked (matched %s)", where)
	}

	// THE DANGEROUS DIRECTION. Everything above is about refusing evidence; if
	// the refusal goes too far the gate reports tested surface as untested, and
	// the fix for THAT looks like adding a waiver — which is the same wrong claim
	// with the sign flipped. So: the invocation forms must still count, and they
	// must report where.
	invoked := "package main\n" +
		"func TestX(t *testing.T) { root.SetArgs([]string{\"ssh\", \"enroll\"}) }\n"
	where := cliReferenced(goFile(t, invoked), "ssh enroll")
	if where == "" {
		t.Fatal("a quoted-arg invocation no longer counts — the gate is now blind to real tests")
	}
	if !strings.HasSuffix(where, ":2") {
		t.Errorf("the reported location should be the line that invokes it, got %q", where)
	}

	// Shell suites: there, the human spelling IS the invocation.
	if where := cliReferenced(shFile(t, "#!/bin/sh\ndejima ssh enroll --yes\n"), "ssh enroll"); where == "" {
		t.Error("a shell invocation stopped counting")
	}
	// ...but a whole-line comment in one still does not.
	if where := cliReferenced(shFile(t, "#!/bin/sh\n# dejima ssh enroll would fix it\ntrue\n"), "ssh enroll"); where != "" {
		t.Errorf("a shell comment counts as invoked (matched %s)", where)
	}
	// The Go rule does not leak into shell files and vice versa: a Go file
	// containing the shell spelling is the #335 case, already asserted above; a
	// shell file containing the Go quoted-arg form is not an invocation either.
	if where := cliReferenced(shFile(t, "#!/bin/sh\necho '\"ssh\", \"enroll\"'\n"), "ssh enroll"); where != "" {
		t.Errorf("a quoted-arg sequence inside a shell script counts as invoked (matched %s)", where)
	}

	// The Go scanner decides what a comment is, not a regex: an invocation is
	// not stripped just because a "//" appears in a string near it.
	inString := "package main\n" +
		"func TestY(t *testing.T) { u := \"http://x/y\" ; root.SetArgs([]string{\"ssh\", \"enroll\"}) ; _ = u }\n"
	if where := cliReferenced(goFile(t, inString), "ssh enroll"); where == "" {
		t.Error("an invocation next to a URL was stripped — the stripper is guessing, not parsing")
	}

	// API operations: a path in a string literal IS how a Go test reaches a
	// route, so that must still count — but prose about one must not.
	apiProse := "package main\n// GET /v1/islands/{name}/audit is not exercised anywhere.\n"
	if where := apiReferenced(goFile(t, apiProse), apiOp{method: "GET", path: "/v1/islands/{name}/audit"}); where != "" {
		t.Errorf("an API path named only in a comment counts as covered (matched %s)", where)
	}
	apiCall := "package main\n" +
		"func TestA(t *testing.T) { http.Get(srv.URL + fmt.Sprintf(\"/v1/islands/%s/audit\", n)) }\n"
	if where := apiReferenced(goFile(t, apiCall), apiOp{method: "GET", path: "/v1/islands/{name}/audit"}); where == "" {
		t.Error("a route reached through a string literal stopped counting")
	}

	// Unparseable Go is an ERROR, never a silent fall back to the raw text:
	// falling back would restore both bugs in the one file nobody is watching.
	if _, err := corpusText("broken_test.go", "package main\nfunc (\n\"unterminated\n", true); err == nil {
		t.Error("a file that cannot be stripped must fail the gate, not be scanned raw")
	}
}
