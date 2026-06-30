package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/api"
)

// nextImmediateFailures drives the give-up backstop: instant drops accumulate,
// a healthy session resets the count, so an unrecoverable open failure stops
// instead of looping forever.
func TestNextImmediateFailures(t *testing.T) {
	if got := nextImmediateFailures(0, time.Millisecond); got != 1 {
		t.Errorf("first instant drop: got %d, want 1", got)
	}
	if got := nextImmediateFailures(3, 10*time.Millisecond); got != 4 {
		t.Errorf("instant drop accumulates: got %d, want 4", got)
	}
	if got := nextImmediateFailures(3, minHealthyUptime); got != 0 {
		t.Errorf("a healthy session resets the count: got %d, want 0", got)
	}
	if got := nextImmediateFailures(3, time.Hour); got != 0 {
		t.Errorf("a long session resets the count: got %d, want 0", got)
	}
	// The loop gives up once the count reaches the cap — sanity-check the policy
	// converges from zero in a bounded number of instant drops.
	n := 0
	for i := 0; i < 100 && n < maxImmediateReconnects; i++ {
		n = nextImmediateFailures(n, time.Millisecond)
	}
	if n != maxImmediateReconnects {
		t.Errorf("instant drops should reach the cap; got %d, want %d", n, maxImmediateReconnects)
	}
}

// Ctrl-C (ETX) in the reconnect wait aborts the loop — in raw mode there's no
// SIGINT, so without this the operator can't escape a reconnecting session.
func TestReconnectSession_CtrlCAborts(t *testing.T) {
	dial := func(context.Context) (*websocket.Conn, error) {
		return nil, errors.New("connection refused") // never succeeds
	}
	stdin := make(chan []byte, 1)
	stdin <- []byte{0x03} // Ctrl-C
	conn, err := reconnectSession(context.Background(), dial, nil, stdin)
	if conn != nil {
		t.Fatal("expected no connection on abort")
	}
	if !errors.Is(err, errSessionAborted) {
		t.Fatalf("expected errSessionAborted, got %v", err)
	}
}

// Ordinary keystrokes typed while disconnected are discarded (nowhere to send),
// NOT mistaken for an abort: the loop keeps retrying.
func TestReconnectSession_NonCtrlCKeepsRetrying(t *testing.T) {
	var calls int32
	dial := func(context.Context) (*websocket.Conn, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("connection refused")
	}
	stdin := make(chan []byte, 8)
	stdin <- []byte("hello") // not Ctrl-C
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := reconnectSession(ctx, dial, nil, stdin)
	if conn != nil || err != nil {
		t.Fatalf("non-Ctrl-C input should not abort; got conn=%v err=%v", conn, err)
	}
	if n := atomic.LoadInt32(&calls); n < 1 {
		t.Errorf("expected the loop to keep dialing despite keystrokes, got %d", n)
	}
}

// A positive session-gone signal stops the reconnect loop promptly (don't retry
// a purged island forever).
func TestReconnectSession_SessionGoneStops(t *testing.T) {
	dial := func(context.Context) (*websocket.Conn, error) {
		return nil, fmt.Errorf("dial session: %w", api.ErrSessionGone)
	}
	conn, err := reconnectSession(context.Background(), dial, nil, nil)
	if conn != nil {
		t.Fatal("expected no connection")
	}
	if !errors.Is(err, api.ErrSessionGone) {
		t.Fatalf("expected ErrSessionGone, got %v", err)
	}
}

// A transport failure (daemon unreachable) is NOT a give-up: the loop keeps
// retrying for as long as the terminal is open. We prove it by cancelling the
// context and asserting it ended as (nil, nil) after multiple dials — never a
// timed-out error.
func TestReconnectSession_TransportRetriesUntilCancel(t *testing.T) {
	var calls int32
	dial := func(context.Context) (*websocket.Conn, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("connection refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn, err := reconnectSession(ctx, dial, nil, nil)
	if conn != nil || err != nil {
		t.Fatalf("transport-down should end as (nil,nil) on cancel; got conn=%v err=%v", conn, err)
	}
	if n := atomic.LoadInt32(&calls); n < 2 {
		t.Errorf("expected the loop to keep retrying (>=2 dials), got %d", n)
	}
}
