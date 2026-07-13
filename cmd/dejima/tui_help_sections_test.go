package main

import (
	"strings"
	"testing"
)

// TestHelpSectionsAndKeys asserts the help overlay is organized into the four
// flat sections and documents every key — including the ones that used to be
// hidden behind the [a] advanced toggle (I) or undocumented entirely (X/v), and
// the update-safety scheme: u/U update the client, the Server menu [H] holds the
// daemon update + SSH setup.
func TestHelpSectionsAndKeys(t *testing.T) {
	help := plain((tuiModel{width: 100}).renderHelp())

	for _, section := range []string{"Island controls", "Team controls", "Server controls", "TUI controls"} {
		if !strings.Contains(help, section) {
			t.Errorf("help missing section %q", section)
		}
	}

	// Keys that must be discoverable straight from `?` (no toggle).
	wants := map[string]string{
		"I":          "invite a teammate",                // was buried under [a] advanced
		"X":          "remove the highlighted",           // was undocumented
		"v":          "provider / model",                 // was undocumented
		"ssh":        "set up SSH fleet-wide",            // SSH setup now lives in the Server menu
		"u/U":        "update Dejima — the client first", // u/U → client, then daemon (gated)
		"servermenu": "server menu — update daemon",      // the Server menu [H] itself
		"O":          "owner lens",                       // multi-tenant
		"%":          "host utilization",                 // aggregate
	}
	for key, phrase := range wants {
		if !strings.Contains(help, phrase) {
			t.Errorf("help missing the [%s] entry (expected phrase %q)", key, phrase)
		}
	}

	// The basic/advanced toggle is gone — everything shows at once.
	if strings.Contains(help, "advanced commands") {
		t.Errorf("help still references the removed advanced toggle")
	}
}

// TestHelpMoreDropdown: the reference (glyph legend + shell CLI) is collapsed
// behind [a] by default so the default help stays short and shows only keys;
// expanding reveals it. Key sections are visible in both states.
func TestHelpMoreDropdown(t *testing.T) {
	collapsed := plain((tuiModel{width: 100}).renderHelp())
	if !strings.Contains(collapsed, "Island controls") {
		t.Error("collapsed help must still show the key sections")
	}
	if strings.Contains(collapsed, "dejima init --repo") {
		t.Error("collapsed help must hide the shell reference")
	}
	if !strings.Contains(collapsed, "more") {
		t.Error("collapsed help should offer the [a] more affordance")
	}

	expanded := plain((tuiModel{width: 100, helpMore: true}).renderHelp())
	if !strings.Contains(expanded, "dejima init --repo") {
		t.Error("expanded help should reveal the shell reference")
	}
	if !strings.Contains(expanded, "Island controls") {
		t.Error("expanded help should still show the key sections")
	}
}
