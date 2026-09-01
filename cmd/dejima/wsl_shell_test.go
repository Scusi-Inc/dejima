package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Every script `dejima wsl setup` runs inside the distro must parse under DASH.
//
// wsl.Run executes them through `sh -c`, and /bin/sh on Ubuntu is dash, not
// bash. A bashism there fails on the operator's machine and not on ours, which
// is the worst place for the difference to live.
//
// It already happened: a wait loop written as a seq expansion produced
//
//	sh: 11: Syntax error: word unexpected (expecting "do")
//
// on a distro with no coreutils — the expansion is empty, leaving an empty
// for-list. It fired AFTER everything else in the install had worked, so a
// clean path had one last landmine at the end, and the message names a shell
// nobody knew they were using.
//
// Parsing is not execution, so this cannot catch a missing binary. It catches
// the class that actually bit: syntax the author's shell accepts and the
// distro's does not.
func TestWSLScriptsParseUnderDash(t *testing.T) {
	if _, err := exec.LookPath("dash"); err != nil {
		t.Skip("dash not installed here; CI covers it")
	}
	src := readSource(t, "wsl.go")

	// The scripts are Go raw-string literals passed to wsl.Run. Raw strings are
	// used precisely because these contain quotes and $, so a backtick inside one
	// terminates the literal early — which broke the build once while writing the
	// very comment explaining the dash bug.
	re := regexp.MustCompile("(?s)wsl\\.Run\\(ctx, distro, `(.*?)`\\)")
	found := re.FindAllStringSubmatch(src, -1)
	if len(found) == 0 {
		t.Fatal("no in-distro scripts found — the extraction pattern no longer matches, " +
			"so this guard is checking nothing while passing")
	}
	for i, m := range found {
		script := m[1]
		cmd := exec.Command("dash", "-n", "-c", script)
		var errOut strings.Builder
		cmd.Stderr = &errOut
		if err := cmd.Run(); err != nil {
			t.Errorf("in-distro script %d is not valid dash: %v\n%s\n--- script ---\n%s",
				i+1, err, errOut.String(), script)
		}
	}
	t.Logf("checked %d in-distro script(s) under dash", len(found))

	// PARSING IS NOT ENOUGH, and the bug that prompted this file proves it:
	// `for i in $(seq 1 60); do` is VALID dash and fails only at runtime, when
	// seq is absent and the expansion is empty. `dash -n` accepts it happily —
	// a mutation restoring the original bug passed this test until this check
	// existed.
	//
	// So ban the constructs whose failure is a missing binary rather than bad
	// syntax. A minimal distro is the target: WSL images ship without much, and
	// "it worked on mine" is how the original landed.
	for i, m := range found {
		for _, banned := range []struct{ tok, why string }{
			{"$(seq ", "seq is coreutils; on a minimal distro it expands to nothing, " +
				"leaving an empty for-list that dash rejects at runtime. Use a POSIX " +
				"counter: i=0; while [ \"$i\" -lt N ]; do … i=$((i + 1)); done"},
			{"`seq ", "same as $(seq …)"},
		} {
			if strings.Contains(m[1], banned.tok) {
				t.Errorf("in-distro script %d uses %q: %s", i+1, banned.tok, banned.why)
			}
		}
	}
}

// install.sh must never stop and ask a question inside `dejima wsl setup`.
//
// The installer asks SERVER or CLIENT on /dev/tty, and it asks there because a
// `curl … | bash` pipe is not evidence that nobody is watching. This call is a
// non-interactive child: if a terminal is reachable from inside the distro, the
// install would wait for an answer nobody is there to give, and `wsl setup`
// would hang with no output explaining why.
func TestWSLInstallAnswersTheInstallerQuestion(t *testing.T) {
	src := readSource(t, "wsl.go")
	i := strings.Index(src, "func installDejimad")
	if i < 0 {
		t.Fatal("installDejimad not found — renamed, and this guard now checks nothing")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "install.sh") {
		t.Skip("installDejimad no longer shells out to install.sh")
	}
	// The LINE that runs the installer, not the function body. The body contains
	// a comment explaining why DEJIMA_ROLE is set, and that comment satisfied a
	// body-wide Contains check even with the variable deleted from the command —
	// prose passing as code, for the third time in one day.
	var invocation string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(trimmed, "install.sh") {
			invocation = trimmed
		}
	}
	if invocation == "" {
		t.Fatal("found no non-comment line invoking install.sh — the guard cannot " +
			"see the command it is meant to check")
	}
	if !strings.Contains(invocation, "DEJIMA_ROLE") {
		t.Error("installDejimad runs install.sh without pinning DEJIMA_ROLE. That "+
			"installer asks SERVER-or-CLIENT on /dev/tty; if one is reachable from "+
			"inside the distro, `dejima wsl setup` hangs waiting for an answer nobody "+
			"is there to give, with nothing on screen saying so.", invocation)
	}
}

// readSource returns a file from this package. Reading SOURCE rather than
// exercising the function is deliberate: these scripts only run inside a WSL
// distro on Windows, which no test host has — so the choice is a source-level
// guard or no guard at all, and the failures being caught are textual.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
