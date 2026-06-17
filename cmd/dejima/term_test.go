package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeTermDaemon stands up an httptest server and points the CLI's client at it
// via DEJIMA_HOST, so `dejima term` commands exercise the real client wiring.
func fakeTermDaemon(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // isolate ~/.dejima (clientcfg)
	t.Setenv("DEJIMA_TOKEN", "")  // non-token client
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("DEJIMA_HOST", strings.TrimPrefix(srv.URL, "http://"))
}

// runTerm executes `dejima term <args>` capturing stdout. The term commands
// print to os.Stdout directly, so we swap it for a pipe.
func runTerm(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cmd := newTermCmd()
	cmd.SetArgs(args)
	err := cmd.Execute()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), err
}

func TestTermLs(t *testing.T) {
	fakeTermDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/terminals" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"terminals":[{"id":"t1","label":"build","created_at":"2026-06-17T00:00:00Z"},{"id":"t2","created_at":"2026-06-17T00:00:00Z"}]}`))
	})
	out, err := runTerm(t, "ls")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"t1", "build", "t2", "dejima-term-t1"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output missing %q in:\n%s", want, out)
		}
	}
}

func TestTermLsEmpty(t *testing.T) {
	fakeTermDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"terminals":[]}`))
	})
	out, err := runTerm(t, "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no host terminals") {
		t.Errorf("empty ls should hint how to create; got:\n%s", out)
	}
}

func TestTermNew(t *testing.T) {
	var gotBody CreateTerminalBody
	fakeTermDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/terminals" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"t3","label":"scratch","created_at":"2026-06-17T00:00:00Z"}`))
	})
	out, err := runTerm(t, "new", "--label", "scratch")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.Label != "scratch" {
		t.Errorf("create body label = %q, want scratch", gotBody.Label)
	}
	for _, want := range []string{"created host terminal t3", "dejima term attach t3"} {
		if !strings.Contains(out, want) {
			t.Errorf("new output missing %q in:\n%s", want, out)
		}
	}
}

func TestTermRm(t *testing.T) {
	hit := false
	fakeTermDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/terminals/t1" {
			hit = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	out, err := runTerm(t, "rm", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("rm did not issue DELETE /v1/terminals/t1")
	}
	if !strings.Contains(out, "removed host terminal t1") {
		t.Errorf("rm output:\n%s", out)
	}
}

// TestTermFeatureDisabled asserts the daemon's 403 (feature off) surfaces as a
// CLI error rather than an empty/confusing success.
func TestTermFeatureDisabled(t *testing.T) {
	fakeTermDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"host terminals are disabled; start dejimad with --host-terminals"}`))
	})
	_, err := runTerm(t, "ls")
	if err == nil {
		t.Fatal("expected an error when host terminals are disabled")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should explain the feature is off; got: %v", err)
	}
}

// CreateTerminalBody mirrors the daemon's CreateTerminalRequest for decoding the
// request the CLI sends (kept local to avoid importing the api test internals).
type CreateTerminalBody struct {
	Label string `json:"label"`
}
