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

// A positive session-gone signal stops the reconnect loop promptly (don't retry
// a purged island forever).
func TestReconnectSession_SessionGoneStops(t *testing.T) {
	dial := func(context.Context) (*websocket.Conn, error) {
		return nil, fmt.Errorf("dial session: %w", api.ErrSessionGone)
	}
	conn, err := reconnectSession(context.Background(), dial, nil)
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

	conn, err := reconnectSession(ctx, dial, nil)
	if conn != nil || err != nil {
		t.Fatalf("transport-down should end as (nil,nil) on cancel; got conn=%v err=%v", conn, err)
	}
	if n := atomic.LoadInt32(&calls); n < 2 {
		t.Errorf("expected the loop to keep retrying (>=2 dials), got %d", n)
	}
}
