package main

import (
	"os"
	"strings"
	"testing"
)

// No prompt in onboard.go may write its own [Y/n] and its own default.
//
// That pairing is the defect #380 removed: the suffix a person READS and the
// branch that decides what empty input MEANS were two separate artifacts, so
// "[y/N] that actually defaults to yes" and "[Y/n] that actually defaults to no"
// both ship silently and both read fine in review. confirmWSL was one; six more
// were still here afterwards, converted alongside it.
//
// confirmDefault derives the suffix FROM the default, so the two cannot
// disagree. This keeps them from growing back in the one file that had them.
//
// SCOPE, stated because a guard that implies more than it checks is its own
// version of this bug: this covers cmd/dejima/onboard.go only. adopt.go,
// eject.go, feedback.go, main.go, ssh.go and uninstall_client.go still build
// their own y/n prompts through different readers. adopt.go's adoptConfirm
// already DERIVES its default from the prompt text, which is the same idea; the
// others are unconverted and this guard does not claim otherwise.
func TestOnboardPromptsDoNotHandRollTheirDefault(t *testing.T) {
	const file = "onboard.go"
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("cannot read %s, so this guard is checking nothing: %v", file, err)
	}
	text := string(src)

	// Positive control: onboard.go must still CALL confirmDefault, not merely
	// define it. Counting occurrences of the name was not enough — onboard.go is
	// where confirmDefault lives, so its own `func` line satisfied a
	// strings.Contains check even with every call site routed elsewhere. The
	// control could detect the function being deleted and not the conversion
	// being undone, which is the case it exists for.
	calls := 0
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "func confirmDefault(") {
			continue // the definition is not a call
		}
		calls += strings.Count(line, "confirmDefault(")
	}
	if calls == 0 {
		t.Fatal("onboard.go defines confirmDefault but no longer calls it — the " +
			"conversion was undone or routed through another name. Either way this " +
			"guard cannot tell a converted file from an unconverted one, so it " +
			"must fail rather than report a clean scan.")
	}

	for i, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "readSingleKey(") {
			continue // readSingleKeyResult( does not match; confirmDefault uses that one
		}
		if !strings.Contains(line, "[Y/n]") && !strings.Contains(line, "[y/N]") {
			continue // a free-text or multi-choice prompt, which is not a confirm
		}
		t.Errorf("%s:%d writes its own default suffix and reads the answer itself, "+
			"so the two can disagree without anyone noticing. Use "+
			"confirmDefault(question, defaultYes):\n  %s", file, i+1, strings.TrimSpace(line))
	}
}
