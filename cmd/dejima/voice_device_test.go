package main

import (
	"strings"
	"testing"
)

// `dejima voice device` is the answer to a Windows-only constraint: ffmpeg's dshow
// input has no "default" microphone, so a name must be chosen and remembered.
// macOS and Linux address a system default and need no picker — but the command
// must still exist and explain itself there rather than erroring, since the TUI
// and docs mention it on every platform.
func TestVoiceDeviceCmdShape(t *testing.T) {
	cmd := newVoiceDeviceCmd()

	if cmd.Name() != "device" {
		t.Errorf("command name = %q, want device", cmd.Name())
	}
	if cmd.Flags().Lookup("set") == nil {
		t.Error("missing --set; there would be no way to choose a microphone non-interactively")
	}
	if !strings.Contains(strings.ToLower(cmd.Short), "microphone") {
		t.Errorf("Short should say what it picks; got %q", cmd.Short)
	}
	// Takes no positional args — the device name carries spaces and parentheses,
	// so it rides on --set rather than being reassembled from argv.
	if cmd.Args == nil {
		t.Error("expected NoArgs; a bare positional would split device names on spaces")
	}
}

// The voice tree must expose all three verbs — install, status, and device —
// since the platform guidance points at each of them by name.
func TestVoiceCmdTree(t *testing.T) {
	requirePaths(t, rootCommandPaths(t),
		"dejima voice install",
		"dejima voice status",
		"dejima voice device",
	)
}

// The unsupported-platform error has to say WHERE voice runs. The old text
// ("isn't supported on this host platform yet") sent an operator driving a
// remote daemon looking for a fix on the daemon host, where there is no
// microphone at all.
func TestUnsupportedPlatformErrorExplainsWhereVoiceRuns(t *testing.T) {
	msg := errUnsupportedPlatform().Error()
	for _, want := range []string{"machine with the microphone", "never leaves"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should explain where voice runs (%q); got %q", want, msg)
		}
	}
}
