package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/pasteimg"
)

type fakeAgentClient struct {
	agents []api.AgentInfo
	err    error
}

func (f fakeAgentClient) ListAgents(_ context.Context, _ string) ([]api.AgentInfo, error) {
	return f.agents, f.err
}

func TestResolveAgentTmux(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	tests := []struct {
		name     string
		agents   []api.AgentInfo
		want     string // requested agent id ("" = default)
		wantTmux string
		wantID   string
		wantErr  bool
	}{
		{
			name: "default picks first attachable",
			agents: []api.AgentInfo{
				{ID: "headless", Attachable: false, Tmux: ""},
				{ID: "main", Attachable: true, Tmux: "dejima-main"},
			},
			wantTmux: "dejima-main", wantID: "main",
		},
		{
			name:     "named agent resolved",
			agents:   []api.AgentInfo{{ID: "a", Attachable: true, Tmux: "ta"}, {ID: "b", Attachable: true, Tmux: "tb"}},
			want:     "b",
			wantTmux: "tb", wantID: "b",
		},
		{
			name:    "named agent missing errors",
			agents:  []api.AgentInfo{{ID: "a", Tmux: "ta"}},
			want:    "zzz",
			wantErr: true,
		},
		{
			name:    "no agents errors",
			agents:  nil,
			wantErr: true,
		},
		{
			name:     "no attachable falls back to first (empty tmux)",
			agents:   []api.AgentInfo{{ID: "h", Attachable: false, Tmux: ""}},
			wantTmux: "", wantID: "h",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeAgentClient{agents: tc.agents}
			tmux, id, err := resolveAgentTmux(cmd, c, "isl", tc.want)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got tmux=%q id=%q", tmux, id)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tmux != tc.wantTmux || id != tc.wantID {
				t.Fatalf("got (%q,%q), want (%q,%q)", tmux, id, tc.wantTmux, tc.wantID)
			}
		})
	}
}

func TestPasteCaptureHint(t *testing.T) {
	if got := pasteCaptureHint(pasteimg.ErrUnsupported); got == nil {
		t.Fatal("unsupported should return advice error")
	}
	if got := pasteCaptureHint(pasteimg.ErrNoImage); got == nil {
		t.Fatal("no-image should return advice error")
	}
	sentinel := errors.New("boom")
	if got := pasteCaptureHint(sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("opaque error should pass through, got %v", got)
	}
}

// Compile-time assertion that the real client satisfies the narrowed interface.
var _ apiClient = (*api.Client)(nil)

func TestBracketedPaste(t *testing.T) {
	const path = "/home/dejima/intake/paste/clip-20260701-180600.png"
	got := bracketedPaste(path)
	want := "\x1b[200~" + path + "\x1b[201~"
	if got != want {
		t.Fatalf("bracketedPaste(%q) = %q, want %q", path, got, want)
	}
	// The framing — not the raw path — is what makes an adapter treat the inject
	// as a paste (→ image attach) rather than typed text (→ literal link).
	if !strings.HasPrefix(got, bpStart) || !strings.HasSuffix(got, bpEnd) {
		t.Fatalf("bracketedPaste output missing DEC 2004 markers: %q", got)
	}
	if !strings.Contains(got, path) {
		t.Fatalf("bracketedPaste dropped the payload path: %q", got)
	}
}
