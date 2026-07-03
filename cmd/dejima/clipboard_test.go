package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func TestOSC52(t *testing.T) {
	got := osc52("hello world")
	if !strings.HasPrefix(got, "\x1b]52;c;") || !strings.HasSuffix(got, "\a") {
		t.Fatalf("osc52 framing wrong: %q", got)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]52;c;"), "\a")
	dec, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("payload not standard base64: %v", err)
	}
	if string(dec) != "hello world" {
		t.Errorf("round-trip = %q, want %q", dec, "hello world")
	}
}

func TestMintedCopyPayload(t *testing.T) {
	// One-paste invite → the two-line install+join block.
	blob := "dejima-invite:abc123"
	m := tuiModel{team: &teamView{
		mintedBlob: blob,
		minted:     &api.CreateTokenResponse{Secret: "sek_ignored"},
	}}
	p, notice := m.mintedCopyPayload()
	if !strings.Contains(p, installClientCmd) {
		t.Errorf("payload missing install line: %q", p)
	}
	if !strings.HasSuffix(p, "dejima join "+blob) {
		t.Errorf("payload should end with the join command, got %q", p)
	}
	if strings.HasSuffix(p, "\n") {
		t.Error("payload must not end in a newline (so the join line waits at the prompt)")
	}
	if lines := strings.Split(p, "\n"); len(lines) != 2 {
		t.Errorf("expected exactly 2 lines (install, join), got %d: %q", len(lines), p)
	}
	if notice == "" {
		t.Error("expected a confirmation notice")
	}

	// No host → no blob → raw secret is copied instead.
	m2 := tuiModel{team: &teamView{minted: &api.CreateTokenResponse{Secret: "sek_y"}}}
	if p2, _ := m2.mintedCopyPayload(); p2 != "sek_y" {
		t.Errorf("no-blob payload = %q, want the raw secret", p2)
	}
}

// TestRenderMintedInvitePanel pins the three onboarding fixes on the rendered
// panel (a live-TTY substitute): install-first quickstart (#208.3), the
// run-on-your-own-computer guidance (#208.2), and the [c] copy affordance (#207.1).
func TestRenderMintedInvitePanel(t *testing.T) {
	m := tuiModel{team: &teamView{
		mintedBlob: "dejima-invite:abc123",
		minted: &api.CreateTokenResponse{
			Token:  api.TokenView{Role: "operator", Label: "alice"},
			Secret: "sek",
		},
	}}
	out := m.renderMintedInvite()
	for _, want := range []string{
		"THEIR OWN computer", // where-to-run guidance (#208.2)
		installClientCmd,     // install-first step (#208.3)
		"dejima join ",       // the join step
		"[c] copy",           // copy hotkey advertised (#207.1)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("minted panel missing %q\n---\n%s", want, out)
		}
	}
}
