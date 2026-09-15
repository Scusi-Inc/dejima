package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The primer is the only thing most agents ever read about this island, and it
// is the one document whose failure mode is completely silent.
//
// It ships INSIDE the image, is written into ~/.claude/CLAUDE.md and
// ~/.codex/AGENTS.md at agent start, and is then read by a machine that will do
// exactly what it says. Nothing checks it. A verb renamed here — `dejima agent`
// to `dejima agents`, say — leaves the primer confidently instructing every
// agent in every island to run a command that does not exist, and the only
// symptom is an agent that quietly concludes it is alone.
//
// That is not hypothetical. On 2026-09-14 the primer told agents to run
// `dejima msg poll` to "see who else is here". That command lists MESSAGES, not
// agents: an agent who has never written to you does not appear in it. An agent
// in a 5-agent island read that, saw nothing, and worked on the assumption it
// had two peers. Two of four workstreams never started. The command was real
// and the sentence was wrong, which is the harder half of this to catch — so
// this guard covers the part a machine can check, and the wording stays a human
// review problem.
func TestIslandPrimerOnlyNamesRealCommands(t *testing.T) {
	primer, err := os.ReadFile(filepath.Join("..", "..", "image", "island-primer.md"))
	if err != nil {
		t.Fatalf("read primer: %v — if it moved, this guard is now checking nothing: %v", err, err)
	}

	verbs := map[string]bool{}
	for _, m := range regexp.MustCompile(`dejima ([a-z][a-z-]*)`).FindAllStringSubmatch(string(primer), -1) {
		verbs[m[1]] = true
	}
	// Control: the primer is supposed to teach an agent how to drive this island.
	// If it names no commands at all, either it was gutted or the regex stopped
	// matching, and a silent pass is exactly the outcome this file exists to
	// prevent.
	if len(verbs) < 3 {
		t.Fatalf("the primer names only %d dejima commands — it either lost its "+
			"examples or this guard stopped parsing it; both pass silently without "+
			"this check", len(verbs))
	}

	known, err := declaredCommands()
	if err != nil {
		t.Fatal(err)
	}
	if len(known) < 20 {
		t.Fatalf("only found %d declared commands — the scan of cmd/dejima is broken, "+
			"and a broken scan makes every primer verb look invalid (or valid, if it "+
			"were inverted). Fix the scan before trusting a result", len(known))
	}

	var missing []string
	for v := range verbs {
		// "dejima" itself appears in prose ("the dejima daemon"); it is not a verb.
		if v == "dejima" {
			continue
		}
		if !known[v] {
			missing = append(missing, v)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the island primer tells every agent in every island to run commands "+
			"that do not exist: %s\n"+
			"The primer is installed into ~/.claude/CLAUDE.md and ~/.codex/AGENTS.md at "+
			"agent start and is read by machines that act on it. A renamed verb here is "+
			"silent — the agent runs the command, it fails, and it improvises.\n"+
			"Either restore the verb or update image/island-primer.md in the same commit.",
			strings.Join(missing, ", "))
	}
}

// The primer must not send agents to `dejima msg poll` to discover their peers.
//
// Pinned as its own check because it is the specific sentence that cost two
// workstreams, and because it reads as correct: `msg poll` IS how you receive
// from peers, so "use it to see who's here" feels like a natural shorthand. It
// is not. Poll returns messages addressed to you; an agent that has not written
// to you is invisible, and an empty poll is indistinguishable from an empty
// island.
//
// `dejima agent ls` is the roster — it lists every agent with its type and
// worktree, which is also what distinguishes a codex peer that Claude Code's
// ListAgents tool cannot see at all.
func TestPrimerPointsAtTheRosterNotTheMailbox(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "image", "island-primer.md"))
	if err != nil {
		t.Fatal(err)
	}
	primer := string(b)

	if !strings.Contains(primer, "dejima agent ls") {
		t.Error("the primer never names `dejima agent ls` — the only command that " +
			"answers \"who else is in this island?\" with every agent, its type and " +
			"its worktree")
	}
	// The failing shape: msg poll offered as the way to find peers.
	for _, bad := range []string{
		"`dejima msg poll` to see who else",
		"dejima msg poll` to see who",
		"msg poll` to see who else is here",
	} {
		if strings.Contains(primer, bad) {
			t.Errorf("the primer tells agents to use the MAILBOX as a ROSTER (%q). "+
				"An agent that has never messaged you does not appear in a poll, so an "+
				"empty result reads as an empty island. Point at `dejima agent ls`.", bad)
		}
	}
	// And the blind spot has to be named, because nothing else will name it: an
	// island agent running codex is invisible to Claude Code's ListAgents.
	if !strings.Contains(primer, "ListAgents") {
		t.Error("the primer does not mention `ListAgents`, so a Claude agent has no " +
			"way to learn that the tool it naturally reaches for shows only Claude " +
			"sessions — and reports no warning when the list is partial")
	}
}

// declaredCommands scans cmd/dejima for cobra `Use:` verbs.
func declaredCommands() (map[string]bool, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`Use:\s+"([a-z][a-z-]*)`)
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = true
		}
	}
	return out, nil
}
