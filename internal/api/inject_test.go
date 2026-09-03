package api

import (
	"context"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime/runtimetest"
)

// A nudge that is TYPED BUT NEVER SUBMITTED is indistinguishable from a working
// one at the error level: both return nil. The operator watched exactly that —
// the message sitting in a codex prompt with the cursor beneath it, turn after
// turn, every message unanswered — so the assertion has to be on the ARGUMENTS.
func TestNudgeIsTypedLiterallyThenSubmitted(t *testing.T) {
	fake := &runtimetest.Fake{}
	s := &Server{rt: fake}
	p := &project.Project{Name: "isl"}
	a := &project.AgentSpec{Tmux: "agent-x"}

	const nudge = "📬 1 new message(s) — run: /usr/local/bin/dejima msg poll"
	if err := s.tmuxInject(context.Background(), p, a, nudge); err != nil {
		t.Fatalf("inject: %v", err)
	}

	var sends [][]string
	for _, c := range fake.ExecCalls() {
		if len(c) > 1 && c[0] == "tmux" && c[1] == "send-keys" {
			sends = append(sends, c)
		}
	}
	if len(sends) != 2 {
		t.Fatalf("expected the text and the submit as SEPARATE send-keys calls, got %d: %v", len(sends), sends)
	}

	text, submit := sends[0], sends[1]

	// -l sends the text LITERALLY. Without it tmux parses each argument as a key
	// NAME first, and nudge text is arbitrary — punctuation, an emoji, a path.
	if !hasArg(text, "-l") {
		t.Errorf("nudge text is not sent literally (-l); tmux will parse it as key names: %v", text)
	}
	if text[len(text)-1] != nudge {
		t.Errorf("the nudge text is not the final argument: %v", text)
	}
	if hasArg(text, "Enter") {
		t.Error("the submit is in the same call as the text; a TUI can process both as one " +
			"event and act on neither — which is how the nudge ended up typed and unsent")
	}

	// The submit must be its own call, and must not carry the text again.
	if submit[len(submit)-1] != "Enter" {
		t.Errorf("second call does not submit: %v", submit)
	}
	if strings.Contains(strings.Join(submit, " "), "message(s)") {
		t.Errorf("the submit call repeats the nudge text: %v", submit)
	}
}

// A headless agent has no tmux session and must be left alone — it collects mail
// by polling, and injecting into nothing would be an error where there is none.
func TestHeadlessAgentIsNotInjected(t *testing.T) {
	fake := &runtimetest.Fake{}
	s := &Server{rt: fake}
	if err := s.tmuxInject(context.Background(), &project.Project{Name: "isl"},
		&project.AgentSpec{Tmux: ""}, "hello"); err != nil {
		t.Fatalf("headless inject should be a no-op, got: %v", err)
	}
	for _, c := range fake.ExecCalls() {
		if len(c) > 1 && c[0] == "tmux" && c[1] == "send-keys" {
			t.Errorf("injected into an agent with no tmux session: %v", c)
		}
	}
}

func hasArg(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
