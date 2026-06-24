package main

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/version"
)

// TestEnvironmentFooterCarriesOnlyVersionAndOS is the privacy guarantee: the
// auto-collected footer must contain version + os/arch and NOTHING that could
// identify the user, host, island, or workload.
func TestEnvironmentFooterCarriesOnlyVersionAndOS(t *testing.T) {
	foot := environmentFooter()

	if !strings.Contains(foot, version.Version) {
		t.Errorf("footer missing version %q:\n%s", version.Version, foot)
	}
	if !strings.Contains(foot, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("footer missing os/arch %q:\n%s", runtime.GOOS+"/"+runtime.GOARCH, foot)
	}

	// Prove no PII / environment leakage. None of these substrates may appear.
	host, _ := os.Hostname()
	forbidden := map[string]string{
		"hostname":      host,
		"$HOME":         os.Getenv("HOME"),
		"$USER":         os.Getenv("USER"),
		"$PATH":         os.Getenv("PATH"),
		"cwd":           mustGetwd(t),
		"DEJIMA_HOST":   "DEJIMA_HOST",
		"DEJIMA_TOKEN":  "DEJIMA_TOKEN",
		"token literal": "token",
		"island word":   "island",
		"/workspace":    "/workspace",
		"~/.dejima":     ".dejima",
	}
	low := strings.ToLower(foot)
	for label, v := range forbidden {
		if v == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(v)) {
			t.Errorf("footer leaks %s (%q):\n%s", label, v, foot)
		}
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func TestComposeIssue(t *testing.T) {
	iss := composeIssue("  port grant fails on a symlink\nmore detail here  ")
	if iss.Title != "port grant fails on a symlink" {
		t.Errorf("title = %q, want first line", iss.Title)
	}
	if !strings.Contains(iss.Body, "more detail here") {
		t.Errorf("body missing user detail: %q", iss.Body)
	}
	if !strings.Contains(iss.Body, environmentFooter()) {
		t.Errorf("body missing environment footer: %q", iss.Body)
	}

	// Empty message → safe default title, footer still present.
	empty := composeIssue("   ")
	if empty.Title != "Feedback" {
		t.Errorf("empty title = %q, want %q", empty.Title, "Feedback")
	}
	if !strings.Contains(empty.Body, runtime.GOOS) {
		t.Errorf("empty body missing footer: %q", empty.Body)
	}
}

func TestClampTitle(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := clampTitle(long)
	if len([]rune(got)) > 121 {
		t.Errorf("clamped title too long: %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("clamped title should end with ellipsis: %q", got)
	}
	if clampTitle("short") != "short" {
		t.Errorf("short title should be unchanged")
	}
}

func TestManualIssueURL(t *testing.T) {
	iss := composeIssue("my title line\nbody")
	u := manualIssueURL(iss)
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("manual URL doesn't parse: %v", err)
	}
	if parsed.Host != "github.com" || parsed.Path != "/"+feedbackRepo+"/issues/new" {
		t.Errorf("manual URL points at wrong place: %s", u)
	}
	q := parsed.Query()
	if q.Get("title") != "my title line" {
		t.Errorf("title query = %q", q.Get("title"))
	}
	if q.Get("labels") != feedbackLabel {
		t.Errorf("labels query = %q, want %q", q.Get("labels"), feedbackLabel)
	}
	if !strings.Contains(q.Get("body"), "body") {
		t.Errorf("body query missing user text: %q", q.Get("body"))
	}
}

func TestIssueURLFromOutput(t *testing.T) {
	cases := map[string]string{
		"https://github.com/aoos/dejima/issues/42\n":        "https://github.com/aoos/dejima/issues/42",
		"Creating issue in aoos/dejima\nhttps://x/issues/7": "https://x/issues/7",
		"weird output no url":                               "weird output no url",
		"https://a/1\nhttps://a/2\n":                        "https://a/2",
	}
	for in, want := range cases {
		if got := feedbackIssueURLFromOutput(in); got != want {
			t.Errorf("feedbackIssueURLFromOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGhReadyMissing verifies the gh-missing path yields an error (the trigger
// for the graceful manual-URL fallback), without invoking any real gh.
func TestGhReadyMissing(t *testing.T) {
	orig := ghLookPath
	t.Cleanup(func() { ghLookPath = orig })
	ghLookPath = func() (string, error) { return "", exec.ErrNotFound }

	if err := ghReady(context.Background()); err == nil {
		t.Fatal("ghReady should fail when gh is not found")
	} else if !strings.Contains(err.Error(), "gh CLI not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCreateIssuePassesSanitizedArgs runs the create path against a fake `gh`
// (a /bin/sh stub) and asserts the exact args carry the title/body/label and
// repo — and that the body carries only version+OS, never identity.
func TestCreateIssuePassesSanitizedArgs(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	orig := ghCommandContext
	t.Cleanup(func() { ghCommandContext = orig })

	var gotArgs []string
	ghCommandContext = func(ctx context.Context, args ...string) *exec.Cmd {
		gotArgs = args
		// Emit a realistic gh success line so URL parsing is exercised.
		return exec.CommandContext(ctx, "sh", "-c", "echo https://github.com/aoos/dejima/issues/99")
	}

	iss := composeIssue("a bug report")
	out, err := createIssue(context.Background(), iss)
	if err != nil {
		t.Fatalf("createIssue: %v", err)
	}
	if out != "https://github.com/aoos/dejima/issues/99" {
		t.Errorf("issue URL = %q", out)
	}

	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"issue", "create", "--repo", feedbackRepo, "--label", feedbackLabel, "--title", "--body"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh args missing %q: %v", want, gotArgs)
		}
	}
	// The body arg must be the composed body: contains the footer, no identity.
	body := argAfter(gotArgs, "--body")
	if !strings.Contains(body, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("--body missing os/arch footer: %q", body)
	}
	if host, _ := os.Hostname(); host != "" && strings.Contains(body, host) {
		t.Errorf("--body leaks hostname: %q", body)
	}
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestFeedbackCommandDryRun drives the actual `feedback` cobra command in
// --dry-run mode: it must show the exact issue (show-before-send), never call
// gh, and print a manual URL — proving the command end-to-end without network.
func TestFeedbackCommandDryRun(t *testing.T) {
	// Isolate the ledger write to a temp HOME so the real ledger isn't touched.
	t.Setenv("HOME", t.TempDir())

	// Fail loudly if --dry-run ever shells out to gh.
	origGh := ghCommandContext
	t.Cleanup(func() { ghCommandContext = origGh })
	ghCommandContext = func(ctx context.Context, args ...string) *exec.Cmd {
		t.Fatalf("dry-run must not invoke gh, got args: %v", args)
		return nil
	}

	out := captureStdout(t, func() {
		// Drive it through the real root so it dispatches as `dejima feedback`.
		root := newRootCmd()
		root.SetArgs([]string{"feedback", "--dry-run", "a", "dry", "run", "report"})
		if err := root.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})

	if !strings.Contains(out, "a dry run report") {
		t.Errorf("output missing the user message:\n%s", out)
	}
	if !strings.Contains(out, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("output missing the os/arch footer:\n%s", out)
	}
	if !strings.Contains(out, "--dry-run: nothing was sent.") {
		t.Errorf("output missing the dry-run notice:\n%s", out)
	}
	if !strings.Contains(out, "github.com/"+feedbackRepo+"/issues/new") {
		t.Errorf("output missing the manual URL:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what was
// written. The feedback command writes its preview to os.Stdout directly.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
