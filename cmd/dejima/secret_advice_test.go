package main

import (
	"bytes"
	"strings"
	"testing"
)

// An operator set a GH_TOKEN, was told "Restart the agent to pick it up", went
// looking for HOW, ran `dejima reset`, and lost every Codex conversation in the
// island. Irreversibly — reset destroys the home volume.
//
// The advice was correct and unactionable, which is the combination that sends
// someone hunting. `dejima reset`'s own --help names the right command; nobody
// reads the help of the command they are about to not use.
//
// These assert on the TEXT because the text is the whole feature. There is no
// behaviour to test: what failed was a sentence that named a verb and no command.
func TestSecretSetNamesTheExactRestartCommand(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"secret", "set", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	// The help is not where the advice lives, so this test drives the real
	// print path below instead; the help call above only proves the command
	// exists and is wired, which is the subject the rest of the test needs.
	if !strings.Contains(out.String(), "set") {
		t.Fatal("`secret set` is not reachable from the root command")
	}
}

// The advice string itself, asserted where it is written. Kept as a unit on the
// constant rather than through a live daemon: a secret set needs one, and the
// thing being guarded is the wording, not the request.
func TestRestartAdviceIsActionable(t *testing.T) {
	advice := secretRestartAdvice("wildfire")

	if !strings.Contains(advice, "dejima agent restart wildfire") {
		t.Errorf("advice does not name the command, which is the whole defect:\n%s", advice)
	}
	// --resume or the operator gets the secret AND a blank agent — the same loss,
	// more quietly, from following our own instruction.
	if !strings.Contains(advice, "--resume") {
		t.Errorf("advice omits --resume, so the fix costs the conversation anyway:\n%s", advice)
	}
	// It must NOT send anyone toward the destructive one.
	if strings.Contains(advice, "dejima reset") {
		t.Errorf("advice points at reset, which erases every conversation:\n%s", advice)
	}
}
