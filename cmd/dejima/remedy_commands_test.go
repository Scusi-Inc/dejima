package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// An error message that names a remedy which does not remedy is worse than a
// generic one: it spends the reader's time and their trust. That happened here —
// a 401 pointed at `dejima github connect`, which adds a second identity and
// changes nothing about which one is used, so the operator ran the suggested fix
// and stayed broken.
//
// The half that can be enforced mechanically is narrower but real: every
// `dejima …` command quoted in a user-facing message must EXIST. A remedy naming
// a subcommand that was renamed or never written is the same failure with a
// cheaper cause, and nothing else in the tree would catch it.
//
// What this cannot check is whether the command is the RIGHT one. That is
// judgement, and the 401 case needed a human to notice. This catches the
// spelling, not the advice.

// Candidates are extracted as COMPLETE spans, not as a prefix followed by prose,
// because a wrong subcommand and trailing prose look identical to a prefix
// matcher. The first version of this guard trimmed to the longest resolvable
// prefix — so `dejima github list-identities` resolved as `dejima github` and
// passed, which is precisely the failure it exists to catch. Verified: that
// mutation survived.
//
// Two shapes cover how these messages are actually written:
//
//	`dejima github ls`            — backticked: the span IS the command
//	...:      dejima github ls\n  — runs to the end of its line
var (
	remedyBacktick = regexp.MustCompile("`dejima ([^`]+)`")
	remedyLineTail = regexp.MustCompile(`dejima ([a-z][a-z0-9- ]*?)(?:\\n|"|$)`)
)

// commandWord is what a cobra command name can look like. Everything else in a
// remedy string is an argument the reader supplies (<name>, %s, --flag, an
// island name) or, in a comment, the tail of a sentence.
//
// Allow-list rather than deny-list, learned the hard way: the first version
// listed the shapes to STOP at and missed `%s` and `//`, which produced three
// false failures. A guard people distrust gets deleted, so its precision is not
// cosmetic.
var commandWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// commandPath returns the leading run of command-shaped words — the part that
// must name a real command.
func commandPath(raw string) []string {
	var out []string
	for _, w := range strings.Fields(raw) {
		if !commandWord.MatchString(w) {
			break
		}
		out = append(out, w)
	}
	return out
}

func TestQuotedRemediesNameCommandsThatExist(t *testing.T) {
	root := newRootCmd()
	files := goSourcesUnder(t, filepath.Join("..", ".."), []string{
		filepath.Join("cmd", "dejima"),
		filepath.Join("internal", "selfupdate"),
	})
	if len(files) == 0 {
		t.Fatal("found no sources to scan — this guard is not reading anything")
	}

	seen := map[string]bool{}
	var bad []string
	scanned := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var spans []string
		for _, m := range remedyBacktick.FindAllStringSubmatch(string(b), -1) {
			spans = append(spans, m[1])
		}
		for _, m := range remedyLineTail.FindAllStringSubmatch(string(b), -1) {
			spans = append(spans, m[1])
		}
		for _, raw := range spans {
			path := commandPath(raw)
			if len(path) == 0 {
				continue
			}
			scanned++
			key := strings.Join(path, " ")
			if seen[key] {
				continue
			}
			seen[key] = true
			if word, isBad := badSubcommand(root, path); isBad {
				bad = append(bad, f+": `dejima "+key+"` — no such subcommand "+strconv.Quote(word))
			}
		}
	}
	// A guard over string literals fails open: rename the commands, change the
	// message style, and it reports all-clear over nothing.
	if scanned == 0 {
		t.Fatal("matched no `dejima <command>` strings at all — this guard is no longer watching anything")
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("these messages name commands that do not exist:\n  %s", strings.Join(bad, "\n  "))
	}
}

// badSubcommand reports whether path names a subcommand that does not exist,
// and the word that does not.
//
// The hard part is that a wrong subcommand and an ARGUMENT are textually
// identical: `github list-identities` and `ssh authorize myisland` are both
// "known command, then a lowercase word". Two earlier versions of this guard
// foundered on that — one trimmed to the longest resolvable prefix and so could
// never see a wrong subcommand at all, the other flagged every argument.
//
// cobra separates them. Find() resolves as far as it can and hands back the
// rest; the leftover is a MISTAKE only when the command it stopped at still has
// subcommands, because a group command takes no arguments of its own. Under a
// leaf, a leftover word is just an argument.
func badSubcommand(root *cobra.Command, path []string) (string, bool) {
	cmd, rest, err := root.Find(path)
	if err != nil || cmd == nil {
		return "", false
	}
	if cmd == root {
		return "", false // prose that happens to follow the word "dejima"
	}
	if len(rest) > 0 && cmd.HasSubCommands() {
		return rest[0], true
	}
	return "", false
}

func goSourcesUnder(t *testing.T, root string, dirs []string) []string {
	t.Helper()
	var out []string
	for _, d := range dirs {
		err := filepath.Walk(filepath.Join(root, d), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}
	return out
}

// The control. The matcher and the resolver both have to be able to say NO, or
// the test above is decoration: a resolver that returned true for everything
// would report a clean sweep over messages naming nothing real.
func TestRemedyGuardRejectsACommandThatDoesNotExist(t *testing.T) {
	root := newRootCmd()
	for _, c := range []struct {
		path []string
		bad  bool
		why  string
	}{
		{[]string{"github", "ls"}, false, "a real subcommand"},
		{[]string{"github", "list-identities"}, true, "a wrong subcommand under a group"},
		{[]string{"ssh", "authorize", "myisland"}, false, "an ARGUMENT under a leaf, not a subcommand"},
		{[]string{"mcp", "grant", "oc-home", "files"}, false, "two arguments under a leaf"},
		{[]string{"to", "apply", "it"}, false, "prose after the word dejima"},
	} {
		if _, got := badSubcommand(root, c.path); got != c.bad {
			t.Errorf("badSubcommand(%v) = %v, want %v — %s", c.path, got, c.bad, c.why)
		}
	}
	if got := remedyBacktick.FindStringSubmatch("run `dejima github default foo` now"); got == nil {
		t.Error("the backtick matcher no longer finds a quoted remedy; the guard is reading nothing")
	}
	if got := commandPath("github list-identities"); len(got) != 2 {
		t.Errorf("commandPath trimmed a real subcommand to %v — a wrong subcommand would "+
			"resolve as its parent and pass", got)
	}
}
