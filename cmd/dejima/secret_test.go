package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func secretSub(t *testing.T, name string) *cobra.Command {
	t.Helper()
	for _, sub := range newSecretCmd().Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	t.Fatalf("`dejima secret %s` not registered", name)
	return nil
}

// The verb set an operator needs to manage a secret's life: `dejima secret set`
// to add or rotate, `dejima secret ls` to see what exists, and
// `dejima secret rm` to remove.
func TestSecretCmdTree(t *testing.T) {
	want := map[string]bool{"set": false, "ls": false, "rm": false}
	for _, sub := range newSecretCmd().Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("`dejima secret %s` not registered", name)
		}
	}
}

// A value typed on a command line lands in shell history and the process list,
// where it outlives the command and is readable by other processes. `set` takes
// the value from a hidden prompt or stdin — never an argument.
func TestSecretSetTakesNoValueArgument(t *testing.T) {
	set := secretSub(t, "set")

	// Exactly two positional args: island and NAME. A third would be the value.
	if err := set.Args(set, []string{"isl", "NAME", "the-value"}); err == nil {
		t.Error("`secret set` accepted a third positional — a value on the command line " +
			"leaks into shell history and the process list")
	}
	if err := set.Args(set, []string{"isl", "NAME"}); err != nil {
		t.Errorf("island + NAME should be valid: %v", err)
	}
	if set.Flags().Lookup("stdin") == nil {
		t.Error("missing --stdin; scripted callers would have no way to supply a value")
	}
}

// The help is where an operator forms their mental model, so it has to carry
// the caveat rather than bury it in docs. Someone who believes these values are
// hidden from agents will store things that don't belong in an agent's
// container — worse than having no feature at all.
func TestSecretHelpStatesAgentsCanReadValues(t *testing.T) {
	long := newSecretCmd().Long
	if !strings.Contains(long, "not a boundary against agents") {
		t.Errorf("help must say this isn't a boundary against agents; got:\n%s", long)
	}
	if !strings.Contains(long, "never shown") && !strings.Contains(long, "never returned") {
		t.Errorf("help should say values are never shown/returned; got:\n%s", long)
	}
	// And it must not oversell itself.
	for _, word := range []string{"vault", "lockbox", "encrypted at rest"} {
		if strings.Contains(strings.ToLower(long), word) {
			t.Errorf("help implies a stronger guarantee than exists (%q):\n%s", word, long)
		}
	}
}

// A rotation only reaches NEW shells; a running process keeps the environment
// it started with. Without saying so, an operator watches their agent keep
// failing with the old value and concludes the feature is broken.
func TestSecretSetMentionsRestart(t *testing.T) {
	set := secretSub(t, "set")
	if !strings.Contains(strings.ToLower(set.Long), "rotate") {
		t.Errorf("`set` help should explain rotation; got:\n%s", set.Long)
	}
}

func TestSecretRemoveTakesIslandAndName(t *testing.T) {
	rm := secretSub(t, "rm")
	if err := rm.Args(rm, []string{"isl"}); err == nil {
		t.Error("`secret rm` should require both an island and a NAME")
	}
	if err := rm.Args(rm, []string{"isl", "NAME"}); err != nil {
		t.Errorf("island + NAME should be valid: %v", err)
	}
}

func TestSecretListTakesAnIsland(t *testing.T) {
	ls := secretSub(t, "ls")
	if err := ls.Args(ls, []string{}); err == nil {
		t.Error("`secret ls` should require an island — secrets are per-island")
	}
	if err := ls.Args(ls, []string{"isl"}); err != nil {
		t.Errorf("one island should be valid: %v", err)
	}
}
