package main

import (
	"strings"
	"testing"
)

// TestHelpSectionsAndKeys asserts the help overlay is organized into the four
// flat sections and documents every key — including the ones that used to be
// hidden behind the [a] advanced toggle (I) or undocumented entirely (X/v/S/U).
func TestHelpSectionsAndKeys(t *testing.T) {
	help := plain((tuiModel{width: 100}).renderHelp())

	for _, section := range []string{"Island controls", "Team controls", "Server controls", "TUI controls"} {
		if !strings.Contains(help, section) {
			t.Errorf("help missing section %q", section)
		}
	}

	// Keys that must be discoverable straight from `?` (no toggle).
	wants := map[string]string{
		"I": "invite a teammate",      // was buried under [a] advanced
		"X": "remove the highlighted", // was undocumented
		"v": "provider / model",       // was undocumented
		"S": "set up SSH fleet-wide",  // was undocumented
		"U": "update Dejima itself",   // was undocumented (distinct from [u])
		"O": "owner lens",             // multi-tenant
		"%": "host utilization",       // aggregate
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
