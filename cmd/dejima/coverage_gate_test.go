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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// testCorpus is the concatenation of every Go test file plus the live shell
// suites under scripts/ — the body of text the gate searches for references.
// Worktrees and per-agent checkouts under .claude/ and .agents/ are excluded so
// a sibling branch can never spoof coverage on this one.
func testCorpus(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
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
		b.Write(data)
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	return b.String()
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

// cliReferenced reports whether the token sequence of a command appears in the
// corpus either as a Go quoted-arg sequence (`"agent", "ls"`) or as a shell
// invocation (`dejima agent ls`).
func cliReferenced(corpus, cmd string) bool {
	toks := strings.Fields(cmd)
	quoted := make([]string, len(toks))
	esc := make([]string, len(toks))
	for i, tk := range toks {
		quoted[i] = regexp.QuoteMeta(`"` + tk + `"`)
		esc[i] = regexp.QuoteMeta(tk)
	}
	if regexp.MustCompile(strings.Join(quoted, `\s*,\s*`)).MatchString(corpus) {
		return true
	}
	return regexp.MustCompile(`dejima\s+` + strings.Join(esc, `\s+`)).MatchString(corpus)
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
// appears in the corpus.
func apiReferenced(corpus string, op apiOp) bool {
	if op.id != "" && strings.Contains(corpus, op.id) {
		return true
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
	return regexp.MustCompile(strings.Join(parts, "/")).MatchString(corpus)
}

// --- waivers ---------------------------------------------------------------

// loadWaivers reads testdata/coverage_waivers.txt: one entry per line, blank
// lines and #-comments ignored. CLI entries are "cli <command path>"; API
// entries are "api METHOD /path".
func loadWaivers(t *testing.T, root string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "cmd", "dejima", "testdata", "coverage_waivers.txt"))
	if err != nil {
		t.Fatalf("read waivers: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
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

	check := func(key string, referenced bool, uncoveredMsg string) {
		if waivers[key] {
			usedWaiver[key] = true
			if referenced {
				staleWaived = append(staleWaived, key+" (now has a test — delete the waiver)")
			}
			return
		}
		if !referenced {
			uncovered = append(uncovered, uncoveredMsg)
		}
	}

	for _, cmd := range cliCommands() {
		check("cli "+cmd, cliReferenced(corpus, cmd),
			"cli "+cmd+"  (cobra command with no referencing test)")
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
