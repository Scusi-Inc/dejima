package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aoos/dejima/internal/clientcfg"
)

// resolveTarget is the single source of truth for the connection target. It must
// honor DEJIMA_HOST first (override + in-island path), then the saved active
// profile (so a remote target survives restarts), then the local socket.
func TestResolveTargetPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // redirect ~/.dejima for clientcfg
	if err := clientcfg.Save(clientcfg.Config{
		Profiles:      []clientcfg.Profile{{Name: "minion", Host: "100.77.85.107:7273"}},
		ActiveProfile: "minion",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("env wins over saved profile", func(t *testing.T) {
		t.Setenv("DEJIMA_HOST", "host.docker.internal:7274")
		host, _, source := resolveTarget()
		if host != "host.docker.internal:7274" || source != "env" {
			t.Fatalf("env should win, got (%q, source=%q)", host, source)
		}
	})

	t.Run("saved active profile when env unset", func(t *testing.T) {
		t.Setenv("DEJIMA_HOST", "")
		host, label, source := resolveTarget()
		if host != "100.77.85.107:7273" || label != "minion" || source != "profile" {
			t.Fatalf("want (100.77.85.107:7273, minion, profile), got (%q, %q, %q)", host, label, source)
		}
	})
}

// A NUL delivered as a single rune is exactly what wedged a saved host into
// `http://\x00minion/...`; printableInput must drop it while keeping ordinary
// characters and spaces.
func TestPrintableInput(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"letter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}}, "m"},
		{"digit", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}}, "7"},
		{"colon", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}, ":"},
		{"space", tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, " "},
		{"nul", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}}, ""},
		{"del", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0x7f}}, ""},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, ""},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, ""},
		{"paste", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("minion")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := printableInput(tc.msg); got != tc.want {
				t.Fatalf("printableInput(%v) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// clientForHost is the choke point: a host carrying a control character must be
// rejected with a clear error rather than producing an unparseable request URL.
func TestClientForHostRejectsControlChars(t *testing.T) {
	if _, err := clientForHost("\x00minion:7273"); err == nil {
		t.Fatal("expected error for host with NUL, got nil")
	} else if !strings.Contains(err.Error(), "control character") {
		t.Fatalf("error should name the cause, got: %v", err)
	}
}
