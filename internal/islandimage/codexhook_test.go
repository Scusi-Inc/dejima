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
