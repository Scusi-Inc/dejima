package main

import (
	"context"
	"io"
	"testing"
	"time"
)

// `dejima link approvals watch` connects to the SSE stream and exits cleanly when
// its context is cancelled (empty queue → only keepalives).
func TestCLIApprovalsWatch(t *testing.T) {
	cliEnv(t) // points DEJIMA_HOST at a live in-proc server

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	root := newRootCmd()
	root.SetArgs([]string{"link", "approvals", "watch"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	if err := root.ExecuteContext(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("approvals watch: %v", err)
	}
}
