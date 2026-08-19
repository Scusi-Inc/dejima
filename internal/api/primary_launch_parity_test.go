package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// An island has TWO launch paths: the daemon launches co-located agents
// (agentLaunchScript), and image/start.sh launches the primary. They diverged —
// only the first sourced the secrets hook — and because most islands have one
// agent and that agent IS the primary, the common case was the broken one.
//
// Nothing compared them, so the divergence was invisible for as long as it
// existed. These tests make it expressible as a failure: the sourcing prologue
// is derived FROM agentLaunchScript and then required to appear in start.sh, so
// renaming the hook or changing the invocation on the daemon side fails here
// until the entrypoint is updated to match.

// sourcePrologue extracts the ". <hook> …; exec" prefix that agentLaunchScript
// puts in front of an interactive agent's launch command.
func sourcePrologue(t *testing.T) string {
	t.Helper()
	got := agentLaunchScript(&project.AgentSpec{ID: "a1", Type: "claude-code"}, false)
	// The prologue is everything from the leading `.` up to and including the
	// `; exec ` that precedes the real launch command.
	re := regexp.MustCompile(`\. /etc/profile\.d/[^\s]+ 2>/dev/null \|\| true; exec `)
	m := re.FindString(got)
	if m == "" {
		t.Fatalf("agentLaunchScript no longer sources a profile.d hook — if that is intentional, "+
			"image/start.sh must change with it. Got: %s", got)
	}
	return m
}

func readStartSh(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "image", "start.sh"))
	if err != nil {
		t.Fatalf("read image/start.sh: %v", err)
	}
	return string(b)
}

// The parity check. The primary's launch path must source the SAME hook, the
// same way, as the co-located agents' path.
func TestPrimaryLaunchSourcesSecretsLikeCoLocatedAgents(t *testing.T) {
	prologue := sourcePrologue(t)
	start := readStartSh(t)

	// Compare on the hook invocation itself, ignoring the trailing `exec ` since
	// the two paths interpolate different launch commands after it.
	invocation := strings.TrimSuffix(prologue, "exec ")
	if !strings.Contains(start, invocation) {
		t.Errorf("image/start.sh does not source the secrets hook the way agentLaunchScript does.\n"+
			"the primary agent would launch without the island's secrets in its environment "+
			"(the \"my secret isn't there\" bug).\nexpected to find: %q", invocation)
	}
}

// The primary's launch must actually go through bash -c, not be handed to tmux
// bare. A future edit that keeps a mention of the hook in a comment but drops
// the wrapper would pass a naive substring check; this asserts the wrapper is
// on the tmux command itself.
func TestPrimaryLaunchIsWrapped(t *testing.T) {
	start := readStartSh(t)
	re := regexp.MustCompile(`tmux new-session[^\n]*`)
	line := re.FindString(start)
	if line == "" {
		t.Fatal("no `tmux new-session` line in image/start.sh")
	}
	// The bare form is `-c "$WORKSPACE" "$LAUNCH"`. Match that specifically:
	// "$LAUNCH" also appears legitimately as the wrapper's argument.
	if strings.Contains(line, `"$WORKSPACE" "$LAUNCH"`) {
		t.Errorf("the primary is launched bare, so it gets no secrets: %s", line)
	}
	if !strings.Contains(line, "launch_with_secrets") {
		t.Errorf("the tmux launch does not go through the secrets wrapper: %s", line)
	}
}

// Behavioural: run start.sh's OWN wrapper function and confirm the command it
// produces puts a secret in the environment of a CHILD process — which is the
// actual requirement, since the agent's tool shells are subprocesses and a fix
// that only satisfies a login shell reproduces the bug.
//
// The hook path is substituted to a temp file (the real one is /etc/profile.d,
// unwritable in a test); everything else — the quoting, the bash -c, the exec —
// is the function's real output.
func TestPrimaryLaunchExportsIntoChildProcesses(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	secrets := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(secrets, []byte("GH_TOKEN=s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(dir, "hook.sh")
	hookBody := "eval \"$(DEJIMA_SECRETS_FILE=" + secrets + " bash " +
		filepath.Join(root, "image", "load-secrets.sh") + ")\" || true\n"
	if err := os.WriteFile(hook, []byte(hookBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pull the wrapper out of start.sh and run it for real.
	script := `
set -euo pipefail
eval "$(sed -n '/^launch_with_secrets()/,/^}/p' ` + filepath.Join(root, "image", "start.sh") + `)"
cmd="$(launch_with_secrets 'bash -c "printenv GH_TOKEN"')"
# Point the wrapper at the test hook instead of /etc/profile.d.
cmd="${cmd//\/etc\/profile.d\/10-dejima-secrets.sh/` + hook + `}"
# Run it the way tmux does: as a shell command string.
sh -c "$cmd"
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("running the primary launch wrapper failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "s3cret" {
		t.Errorf("the secret did not reach a child of the launched process; got %q.\n"+
			"tool subprocesses inherit from the agent's environment, so this is the case that matters", out)
	}
}

// The wrapper must not break on a launch command containing a quote — the
// daemon side uses shSingleQuote for exactly this, and the entrypoint needs the
// equivalent.
func TestPrimaryLaunchQuotesTheLaunchCommand(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := `
set -euo pipefail
eval "$(sed -n '/^launch_with_secrets()/,/^}/p' ` + filepath.Join(root, "image", "start.sh") + `)"
cmd="$(launch_with_secrets "sh -c 'echo quoted-ok'")"
sh -c "$cmd"
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("a quoted launch command broke the nesting: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "quoted-ok" {
		t.Errorf("got %q, want quoted-ok", out)
	}
}
