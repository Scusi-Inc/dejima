package islandimage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The codex notify hook must actually POST the event Codex hands it.
//
// It never did. The payload was read as
//
//	payload_json="${1:-{}}"
//
// where the first `}` closes the expansion — so a supplied argument came back
// with a stray `}` glued on, jq rejected it, `set -e` exited 2, and nothing was
// sent. The no-argument default was the only input that parsed, and Codex never
// sends that. The hook worked exactly when it had nothing to report.
//
// Nothing caught it: shellcheck passes the line clean, and no test ran the
// script. The visible symptom was a Codex agent showing no state at all in the
// dashboard while it sat waiting on an approval prompt.
//
// So this runs the REAL script against a stub daemon. Asserting on the source
// text would restate the fix; the failure was behavioural and only executing it
// proves the payload survives the trip.
func TestCodexNotifyHookPostsWhatCodexGivesIt(t *testing.T) {
	for _, bin := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available here; the hook needs it (CI ubuntu/macOS have it)", bin)
		}
	}
	script := filepath.Join("..", "..", "image", "agents", "codex", "hooks", "notify.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the codex notify hook is missing, so this guard checks nothing: %v", err)
	}

	for _, tc := range []struct {
		name     string
		arg      string // "" means invoke with no argument at all
		wantType string
	}{
		{
			// The real thing: the payload Codex passes on a completed turn.
			name:     "a turn-complete payload becomes task-complete",
			arg:      `{"type":"agent-turn-complete","turn-id":"t1","last-assistant-message":"done"}`,
			wantType: "agent.task-complete",
		},
		{
			// Any other Codex event is forwarded under its own name rather than
			// dropped, so a new upstream event type is visible instead of silent.
			name:     "an unrecognised codex event is forwarded, not dropped",
			arg:      `{"type":"session-configured"}`,
			wantType: "agent.codex.session-configured",
		},
		{
			// A payload we cannot parse must still report that something happened.
			// Going quiet is the failure this whole test exists for.
			name:     "an unparseable payload still reports the event",
			arg:      `not json at all`,
			wantType: "agent.codex.unknown",
		},
		{
			name:     "no argument at all",
			arg:      "",
			wantType: "agent.codex.unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type event struct {
				Island  string          `json:"island"`
				Agent   string          `json:"agent"`
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			got := make(chan event, 1)
			bad := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var e event
				if err := json.Unmarshal(body, &e); err != nil {
					bad <- string(body)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				got <- e
				w.WriteHeader(http.StatusAccepted)
			}))
			defer srv.Close()

			args := []string{script}
			if tc.arg != "" {
				args = append(args, tc.arg)
			}
			cmd := exec.Command("bash", args...)
			// A clean environment: inheriting a real DEJIMA_HOST would send this
			// test's events to the operator's daemon instead of the stub.
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"DEJIMA_HOST=" + strings.TrimPrefix(srv.URL, "http://"),
				"DEJIMA_TOKEN=test-token",
				"DEJIMA_PROJECT_NAME=testisland",
				"DEJIMA_AGENT_ID=d9",
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the hook exited %v — Codex gets no event at all.\n%s", err, out)
			}

			select {
			case e := <-got:
				if e.Type != tc.wantType {
					t.Errorf("posted type %q, want %q", e.Type, tc.wantType)
				}
				if e.Island != "testisland" || e.Agent != "d9" {
					t.Errorf("event attributed to island=%q agent=%q, want testisland/d9", e.Island, e.Agent)
				}
			case b := <-bad:
				t.Errorf("the hook posted a body the daemon cannot parse: %s", b)
			default:
				t.Errorf("the hook posted nothing and exited 0 — this is the silent "+
					"failure the fix was for (arg %q)", tc.arg)
			}
		})
	}
}

// A hook that cannot post must leave a trace.
//
// The unconfigured branch was a bare `exit 0`: no output, no file, nothing in a
// log. That made "the hook never ran", "the daemon rejected it" and "the
// autonomy variables are missing from this agent's environment" the same
// observation, which is how a broken hook reads as a missing feature.
//
// Best-effort must still mean traceable. It must ALSO still exit 0 — a hook that
// fails the agent's turn because the daemon is unreachable is a worse bug than
// the silence.
func TestCodexNotifyHookLeavesATraceWhenItCannotPost(t *testing.T) {
	for _, bin := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available here", bin)
		}
	}
	script := filepath.Join("..", "..", "image", "agents", "codex", "hooks", "notify.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the codex notify hook is missing, so this guard checks nothing: %v", err)
	}
	log := filepath.Join(t.TempDir(), "notify.log")

	cmd := exec.Command("bash", script, `{"type":"agent-turn-complete"}`)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"DEJIMA_NOTIFY_LOG=" + log,
		// DEJIMA_HOST and DEJIMA_TOKEN deliberately absent: the unconfigured case.
		"DEJIMA_PROJECT_NAME=testisland",
		"DEJIMA_AGENT_ID=d9",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the hook must not fail the agent's turn when it cannot post: %v\n%s", err, out)
	}

	b, readErr := os.ReadFile(log)
	if readErr != nil {
		t.Fatalf("the hook posted nothing and recorded nothing — an operator has no "+
			"way to tell a broken hook from an absent feature: %v", readErr)
	}
	if !strings.Contains(string(b), "DEJIMA_HOST") {
		t.Errorf("the trace does not name the missing variables, so it does not "+
			"tell the operator what to fix. got: %s", b)
	}
}

// The `notify` key must land ABOVE every table header in config.toml.
//
// init.sh appended it. In TOML a bare key belongs to whatever table header
// precedes it, so appending to a config that ends in a [table] — which every
// real config does — files `notify` under that table and leaves the top-level
// key Codex reads absent. The agent then emits nothing, silently, with the hook
// installed and the word "notify" plainly present in the file.
//
// This runs the REAL block out of init.sh rather than asserting on its text: the
// bug was in what the file ends up looking like, and a source assertion would
// just restate the patch. Running all of init.sh is not an option (it copies
// from /opt/dejima and mutates $HOME), so the block is extracted and executed —
// the same approach the WSL dash guard uses on that file's in-distro scripts.
func TestCodexInitPutsNotifyAboveAnyTableHeader(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available here")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "image", "agents", "codex", "init.sh"))
	if err != nil {
		t.Fatalf("read init.sh: %v", err)
	}
	block := extractNotifyBlock(t, string(src))

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	// An operator config copied from the host: top-level keys, then a table.
	// This is the shape the surrounding code exists to preserve.
	const existing = "model = \"gpt-5\"\n\n[projects.\"/workspace\"]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(cfg, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "-c", "set -euo pipefail\nHOME_CODEX="+dir+"\n"+block)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the notify block failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	notifyAt, tableAt := -1, -1
	for i, line := range strings.Split(string(got), "\n") {
		trimmed := strings.TrimSpace(line)
		if notifyAt < 0 && strings.HasPrefix(trimmed, "notify") {
			notifyAt = i
		}
		if tableAt < 0 && strings.HasPrefix(trimmed, "[") {
			tableAt = i
		}
	}
	if notifyAt < 0 {
		t.Fatalf("no notify key was written at all:\n%s", got)
	}
	if tableAt >= 0 && notifyAt > tableAt {
		t.Errorf("notify landed on line %d, below the table header on line %d — TOML "+
			"reads it as a key of that table, so Codex sees no top-level notify and "+
			"the agent reports nothing:\n%s", notifyAt, tableAt, got)
	}
	// The operator's own settings must survive.
	if !strings.Contains(string(got), "trust_level") || !strings.Contains(string(got), "model") {
		t.Errorf("the existing config was not preserved:\n%s", got)
	}
}

// extractNotifyBlock pulls the config-writing block out of init.sh: from the
// CONFIG= assignment through the `fi` that closes it. It FAILS rather than
// skips when it cannot find the block — a guard that cannot locate its subject
// is not passing, it is checking nothing.
func extractNotifyBlock(t *testing.T, src string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "CONFIG=") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("init.sh no longer assigns CONFIG= — this guard cannot find the " +
			"block it checks, so it must fail rather than pass quietly")
	}
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "fi" {
			block := strings.Join(lines[start:i+1], "\n")
			if !strings.Contains(block, "NOTIFY_LINE") {
				t.Fatalf("the extracted block does not mention NOTIFY_LINE; the "+
					"extraction is picking up the wrong region:\n%s", block)
			}
			return block
		}
	}
	t.Fatal("no closing fi found after CONFIG=")
	return ""
}
