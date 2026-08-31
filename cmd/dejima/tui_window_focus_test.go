package main

import "testing"

// Each singleton opener ran `tmux new-window` unconditionally, so pressing the
// key twice produced two windows instead of returning to the first. Reported
// from a real session as tmux "adding layers of github-connect" — every layer an
// abandoned device-flow poll, none distinguishable from the live one.
func TestTmuxWindowIndexFindsAnExistingWindow(t *testing.T) {
	const list = "0\tclaude\n1\tgithub-connect\n2\thost/api\n"

	if idx, ok := tmuxWindowIndex(list, "github-connect"); !ok || idx != "1" {
		t.Errorf("tmuxWindowIndex(github-connect) = %q,%v; want 1,true", idx, ok)
	}
	if _, ok := tmuxWindowIndex(list, "wsl-setup"); ok {
		t.Error("claimed to find a window that isn't open — the opener would never spawn it")
	}
}

// tmux's own `-t <name>` targeting falls back to prefix and fnmatch searching.
// Selecting the WRONG existing window is worse than opening a new one: the
// operator ends up staring at someone else's session believing it is theirs.
func TestTmuxWindowIndexDoesNotPrefixMatch(t *testing.T) {
	const list = "0\tclaude\n1\thost/api-staging\n"

	if idx, ok := tmuxWindowIndex(list, "host/api"); ok {
		t.Errorf("prefix-matched %q onto host/api-staging (index %s)", "host/api", idx)
	}
}

// Window names can contain the separator-adjacent characters our titles use
// ("host/<id>", "ui-<island>"); only a tab separates index from name.
func TestTmuxWindowIndexHandlesNamesWithPunctuation(t *testing.T) {
	const list = "3\tui-my-island\n4\thost/term-7f339b05bb3a\n"

	if idx, ok := tmuxWindowIndex(list, "host/term-7f339b05bb3a"); !ok || idx != "4" {
		t.Errorf("tmuxWindowIndex = %q,%v; want 4,true", idx, ok)
	}
}

// Empty or malformed output must read as "no such window" so the opener falls
// through and spawns one, rather than silently doing nothing.
func TestTmuxWindowIndexTreatsGarbageAsAbsent(t *testing.T) {
	for _, list := range []string{"", "\n\n", "no-tab-here", "0 claude"} {
		if _, ok := tmuxWindowIndex(list, "claude"); ok {
			t.Errorf("found a window in %q", list)
		}
	}
}
