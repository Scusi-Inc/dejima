package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A CLIENT DEADLINE THAT UNDERCUTS A SERVER BUDGET IS A BUG THAT CANNOT RETRY
// ITS WAY OUT.
//
// Adding a codex agent to an island with a stale image timed out, every time,
// forever. Three numbers had to agree and none of them knew about the others:
//
//	binaryRepairBudget   3m    server: `npm install -g @openai/codex@latest`
//	http.Client.Timeout  30s   client: a HARD cap that overrides the context
//	TUI ctx deadline     60s   caller: bigger than 30s, smaller than 3m — and
//	                           never reached, because the 30s cap fired first
//
// The client hung up at 30s, which cancelled the request context, which killed
// the install mid-write, which left the binary exactly as broken as before. The
// next attempt repeated it identically. Nothing self-corrected and nothing
// escalated; the operator saw `daemon unreachable` and an agent stuck in
// `error`.
//
// client.go:doLong already existed for precisely this hazard, with a comment
// saying so, and clone/panic already used it. AddAgent still did not. The
// comment was not the missing piece — the arithmetic was never checked
// anywhere, so these tests check it. See docs/testing/guards-need-controls.md.

// TestAddAgentBudgetExceedsServerWorstCase is the arithmetic the three numbers
// above never did. It compares constants rather than scanning text, so raising
// binaryRepairBudget without raising AddAgentBudget fails here.
func TestAddAgentBudgetExceedsServerWorstCase(t *testing.T) {
	worst := binaryProbeBudget + binaryRepairBudget
	if AddAgentBudget <= worst {
		t.Fatalf("AddAgentBudget = %v but a single add can legitimately spend %v "+
			"(binaryProbeBudget %v + binaryRepairBudget %v). The caller would hang up "+
			"mid-repair and the agent would never come up.",
			AddAgentBudget, worst, binaryProbeBudget, binaryRepairBudget)
	}
	// Headroom, not a hairline pass: the add also loads the project, writes the
	// spec, execs tmux and re-probes. A budget that clears the repair by a second
	// is a budget that fails on a slow host.
	if margin := AddAgentBudget - worst; margin < 30*time.Second {
		t.Errorf("AddAgentBudget clears the server worst case by only %v — "+
			"leave at least 30s for the rest of the add", margin)
	}
}

// TestAddAgentUsesDoLong pins the transport. AddAgentBudget is inert if the
// request still goes through the 30s-capped default client: the cap overrides
// the context, so the caller's generous deadline is never consulted and the
// test above passes while the bug is fully present.
func TestAddAgentUsesDoLong(t *testing.T) {
	body := funcBody(t, filepath.Join(".", "client.go"), "AddAgent")
	if strings.Contains(body, "c.do(") {
		t.Errorf("AddAgent calls c.do — the 30s hard cap overrides the caller's "+
			"context and truncates an in-band binary repair. Use c.doLong.\n%s", body)
	}
	if !strings.Contains(body, "c.doLong(") {
		t.Errorf("AddAgent no longer calls c.doLong:\n%s", body)
	}
}

// TestAddAgentCallersUseTheSharedBudget keeps callers from inventing a number.
// The TUI's hand-rolled 60s is the reason this gate exists — it looked generous
// and was wrong in both directions at once.
func TestAddAgentCallersUseTheSharedBudget(t *testing.T) {
	root := filepath.Join("..", "..", "cmd", "dejima")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`\.AddAgent\(([A-Za-z_][A-Za-z0-9_.]*)\s*,`)
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, m := range call.FindAllStringSubmatch(text, -1) {
			found++
			ctxVar := m[1]
			// The ctx handed to AddAgent must be derived from AddAgentBudget. We
			// check the file declares that derivation for this variable rather than
			// tracing dataflow — a deliberately shallow check, but it catches the
			// literal-duration mistake that actually happened.
			decl := regexp.MustCompile(regexp.QuoteMeta(ctxVar) +
				`, cancel := context\.WithTimeout\([^,]+, api\.AddAgentBudget\)`)
			if !decl.MatchString(text) {
				t.Errorf("%s: AddAgent(%s, …) — %s is not bounded by api.AddAgentBudget. "+
					"Do not hand-roll a duration here; the server's repair budget is the "+
					"constraint and only AddAgentBudget tracks it.", path, ctxVar, ctxVar)
			}
		}
	}
	// A gate with no subject passes forever. See docs/testing/guards-need-controls.md.
	if found == 0 {
		t.Fatal("found no AddAgent call sites under cmd/dejima — this gate lost its subject")
	}
}

// funcBody returns the source text of a top-level func or method with the given
// name, from the opening brace to the closing brace in column 1.
func funcBody(t *testing.T, path, name string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	sig := regexp.MustCompile(`^func (\([^)]*\) )?` + regexp.QuoteMeta(name) + `\(`)
	for i, ln := range lines {
		if !sig.MatchString(ln) {
			continue
		}
		for j := i; j < len(lines); j++ {
			if lines[j] == "}" {
				return strings.Join(lines[i:j+1], "\n")
			}
		}
	}
	t.Fatalf("could not find func %s in %s", name, path)
	return ""
}
