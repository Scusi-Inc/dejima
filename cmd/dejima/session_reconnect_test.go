package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/coder/websocket"
)

// classifySessionClose drives the auto-reconnect decision: a clean server close
// ends the client; any abnormal drop reconnects to the persistent tmux session.
func TestClassifySessionClose(t *testing.T) {
	bg := context.Background()
	cancelled, cancel := context.WithCancel(bg)
	cancel()

	cases := []struct {
		name string
		err  error
		ctx  context.Context
		want sessReason
	}{
		{"normal closure (detach / agent exit) → exit", websocket.CloseError{Code: websocket.StatusNormalClosure}, bg, sessExitClean},
		{"service restart (daemon self-update) → reconnect", websocket.CloseError{Code: websocket.StatusServiceRestart}, bg, sessReconnect},
		{"going away (daemon restart) → reconnect", websocket.CloseError{Code: websocket.StatusGoingAway}, bg, sessReconnect},
		{"abnormal closure (1006) → reconnect", websocket.CloseError{Code: websocket.StatusAbnormalClosure}, bg, sessReconnect},
		{"transport error (EOF) → reconnect", io.EOF, bg, sessReconnect},
		{"generic transport error → reconnect", errors.New("connection reset by peer"), bg, sessReconnect},
		{"cancelled context wins → exit", io.EOF, cancelled, sessExitCtx},
	}
	for _, tc := range cases {
		if got := classifySessionClose(tc.err, tc.ctx); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
