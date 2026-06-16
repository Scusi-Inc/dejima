package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestApplyFixesRunsConfirmedRemedies(t *testing.T) {
	var calls [][]string
	orig := doctorExec
	doctorExec = func(_ context.Context, argv []string, _ io.Writer) error {
		calls = append(calls, argv)
		return nil
	}
	defer func() { doctorExec = orig }()

	r := &doctorReport{}
	r.add("System", "ok-row", "OK", "fine", "")
	r.add("System", "info-row", "INFO", "fyi", "")
	r.addFix("System", "fixable", "WARN", "needs work", "do the thing",
		&doctorRemedy{desc: "fix it", argv: []string{"loginctl", "enable-linger", "me"}})

	var out bytes.Buffer
	// yes=true → run without prompting.
	if err := r.applyFixes(context.Background(), &out, strings.NewReader(""), true); err != nil {
		t.Fatalf("applyFixes: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 remedy run, got %d: %v", len(calls), calls)
	}
	if got := strings.Join(calls[0], " "); got != "loginctl enable-linger me" {
		t.Errorf("ran %q, want loginctl enable-linger me", got)
	}
	// OK/INFO rows must never be touched.
}

func TestApplyFixesSkipsOnDecline(t *testing.T) {
	var calls [][]string
	orig := doctorExec
	doctorExec = func(_ context.Context, argv []string, _ io.Writer) error {
		calls = append(calls, argv)
		return nil
	}
	defer func() { doctorExec = orig }()

	r := &doctorReport{}
	r.addFix("System", "fixable", "WARN", "needs work", "do it",
		&doctorRemedy{desc: "fix it", argv: []string{"echo", "hi"}})

	var out bytes.Buffer
	// Decline at the prompt ("n").
	if err := r.applyFixes(context.Background(), &out, strings.NewReader("n\n"), false); err != nil {
		t.Fatalf("applyFixes: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("declined fix still ran: %v", calls)
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("expected 'skipped' in output, got %q", out.String())
	}
}

func TestApplyFixesNothingToDo(t *testing.T) {
	r := &doctorReport{}
	r.add("System", "ok", "OK", "fine", "")
	var out bytes.Buffer
	if err := r.applyFixes(context.Background(), &out, strings.NewReader(""), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to auto-fix") {
		t.Errorf("expected 'nothing to auto-fix', got %q", out.String())
	}
}
