package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `dejima local install` and `dejima local pull` against a STUB daemon.
//
// Both were waived as un-invocable on the grounds that running them installs
// Ollama or downloads a multi-gigabyte model. d1's review corrected the layer:
// local.go:101 is `c.LocalInstall(ctx, os.Stdout)` and local.go:159 is
// `c.PullLocalModel(ctx, args[0], os.Stdout)` — both plain HTTP calls. The side
// effect lives on the FAR side of the wire, so a stub answering the endpoint
// exercises the command with no side effect at all.
//
// WHAT THESE TESTS DO NOT COVER, and must never be read as covering: the
// install and the pull themselves. `api POST /v1/local/install` and
// `.../pull` stay waived, because THAT side does need an injectable backend.
// What is covered is the half that has broken repeatedly in this codebase — the
// command reaching the right endpoint, threading its argument, and rendering a
// failure as a failure.
//
// DO NOT ADD A cliEnv TEST TO THIS FILE. The coverage gate credits an API route
// only from a file that can reach it (reachesTheServer, coverage_gate_test.go),
// and that check is FILE-scoped: one cliEnv call anywhere in here would qualify
// the whole file, and the route strings asserted below — which are asserted
// against a stub with no handler behind it — would start crediting the local
// install and pull HANDLERS, which have no test anywhere in the tree. The gate
// would then report their waivers as stale and invite deleting them.
//
// Nothing fails when that happens. It reads as a ratchet tightening. If you
// need cliEnv, put the test in another file — d1 spotted this on review of
// #400, and it is written here rather than in the doc because here is where the
// edit would be made.

// stubDaemon points the CLI at an httptest server with no api.Server behind it,
// so nothing on the daemon side can run. It returns the paths the CLI asked
// for, in order, so a test can assert WHICH endpoint the command reached.
func stubDaemon(t *testing.T, h http.HandlerFunc) *[]string {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		h(w, r)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEJIMA_TOKEN", "")
	t.Setenv("DEJIMA_HOST", ts.URL)

	// THIS IS THE SAFETY PROPERTY OF THE WHOLE FILE, not a formality. On macOS
	// with a LOCAL daemon, `dejima local install` runs the backend's installer
	// in this process (installLocalBackendHere) before it calls the daemon at
	// all — it needs a terminal for the installer's sudo. It skips that only
	// because a host is set. CI builds on macos-latest, so if DEJIMA_HOST ever
	// stopped reaching this, the suite would install Ollama on the runner.
	if resolveHost() == "" {
		t.Fatal("DEJIMA_HOST is not in effect — `local install` would run the real " +
			"installer on this machine; refusing to continue")
	}
	return &seen
}

// TestLocalInstallCommandCallsTheDaemon: the command posts to the install
// endpoint and reports the daemon's success.
func TestLocalInstallCommandCallsTheDaemon(t *testing.T) {
	seen := stubDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "pulling manifest…")
		fmt.Fprintln(w, "--- dejima: local backend installed ---")
	})

	out, err := runCLI(t, "local", "install")
	if err != nil {
		t.Fatalf("`dejima local install` should succeed when the daemon says so: %v", err)
	}
	if len(*seen) == 0 || (*seen)[len(*seen)-1] != "POST /v1/local/install" {
		t.Errorf("wrong endpoint: %v", *seen)
	}
	// The operator's next step is a model; the command has to say so or they
	// have a registered provider and nothing to run on it.
	if !strings.Contains(out, "provider is registered") || !strings.Contains(out, "local pull") {
		t.Errorf("success should name what to do next; got:\n%s", out)
	}
}

// TestLocalPullCommandThreadsTheModelName: the model reaches the URL. An
// argument dropped between the command and the endpoint is the failure this
// catches, and it looks like success from the outside.
func TestLocalPullCommandThreadsTheModelName(t *testing.T) {
	seen := stubDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "downloading 12%…")
		fmt.Fprintln(w, "--- dejima: local model pulled ---")
	})

	out, err := runCLI(t, "local", "pull", "qwen-coder")
	if err != nil {
		t.Fatalf("`dejima local pull` should succeed when the daemon says so: %v", err)
	}
	if len(*seen) == 0 || (*seen)[len(*seen)-1] != "POST /v1/local/models/qwen-coder/pull" {
		t.Errorf("the model name did not reach the endpoint: %v", *seen)
	}
	if !strings.Contains(out, "qwen-coder") {
		t.Errorf("the pull should name what it pulled; got:\n%s", out)
	}
}

// TestLocalPullReportsDaemonFailure: a stream that ends with an ERROR line is a
// failure, not a success with unfortunate text. Reporting a failed pull as done
// is how someone points an agent at a model that is not there.
func TestLocalPullReportsDaemonFailure(t *testing.T) {
	stubDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "downloading 3%…")
		fmt.Fprintln(w, "ERROR: no space left on device")
	})

	if _, err := runCLI(t, "local", "pull", "qwen-coder"); err == nil {
		t.Fatal("a pull that failed on the daemon must fail the command")
	} else if !strings.Contains(err.Error(), "no space left on device") {
		t.Errorf("the daemon's reason should survive to the operator, got: %v", err)
	}
}

// TestLocalInstallReportsHTTPFailure: a 4xx/5xx from the daemon is an error the
// operator sees, not a silent no-op.
func TestLocalInstallReportsHTTPFailure(t *testing.T) {
	stubDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"backend not supported on this host"}`, http.StatusInternalServerError)
	})

	if _, err := runCLI(t, "local", "install"); err == nil {
		t.Fatal("a 500 from the daemon must fail the command")
	}
}
