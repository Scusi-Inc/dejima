package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// wslFuncBody returns one top-level func's source from wsl.go.
func wslFuncBody(t *testing.T, name string) string {
	t.Helper()
	src := readSource(t, "wsl.go")
	i := strings.Index(src, "func "+name+"(")
	if i < 0 {
		t.Fatalf("%s not found in wsl.go — renamed, and this guard now checks nothing", name)
	}
	body := src[i:]
	if j := strings.Index(body, "\n}\n"); j > 0 {
		body = body[:j]
	}
	return body
}

// THE STEP THAT DISAPPEARS IF NOBODY GUARDS IT.
//
// `make setup` built the island image, so the old source-build path got it for
// free. Installing from a release binary skips `make setup` entirely — and a
// distro with a running daemon and no image passes every check `wsl setup`
// makes, then fails on the first `dejima init`. That is the worst place for it:
// setup has already reported success.
//
// It needs no source tree — handleImageBuild materializes a context embedded in
// the daemon binary — so the only thing standing between here and that failure
// is that somebody remembered to ask.
func TestWSLSetupBuildsTheIslandImage(t *testing.T) {
	src := readSource(t, "wsl.go")
	if !strings.Contains(src, "func buildIslandImage(") {
		t.Fatal("buildIslandImage is gone — nothing builds the island image, and the " +
			"first `dejima init` fails after a setup that reported success")
	}
	setup := wslFuncBody(t, "runWSLSetup")
	if !strings.Contains(setup, "buildIslandImage") {
		t.Fatal("the setup flow never calls buildIslandImage")
	}

	// ORDER IS LOAD-BEARING: the DAEMON builds the image, so asking before it is
	// running cannot work. Asserted rather than trusted to reading order.
	iStart := strings.Index(setup, "startDaemonInWSL")
	iBuild := strings.Index(setup, "buildIslandImage")
	if iStart < 0 {
		t.Fatal("startDaemonInWSL is not called in the setup flow — this guard's " +
			"premise has moved and the ordering check below means nothing")
	}
	if iBuild < iStart {
		t.Error("the island image is built BEFORE the daemon starts; the daemon is " +
			"what builds it")
	}
}

// A failed image build must stop setup. Backgrounding it, or ignoring its error,
// moves the failure to the operator's first `dejima init` rather than fixing it.
func TestIslandImageBuildIsBlockingAndFatal(t *testing.T) {
	setup := wslFuncBody(t, "runWSLSetup")
	i := strings.Index(setup, "buildIslandImage")
	if i < 0 {
		t.Fatal("buildIslandImage is not called")
	}
	window := setup[i:]
	if j := strings.Index(window, "\n\n"); j > 0 {
		window = window[:j]
	}
	if strings.Contains(window, "go buildIslandImage") || strings.Contains(window, "go func") {
		t.Error("the island-image build is backgrounded — setup can return before the " +
			"image exists")
	}
	if !strings.Contains(window, "return") {
		t.Error("a failed island-image build does not stop setup")
	}
}

// The archive must be checksum-verified. The reason this path exists at all is a
// link that drops connections mid-transfer; that same link truncates a tarball,
// and an unverified one installs a corrupt daemon that fails later and elsewhere.
func TestDejimadInstallVerifiesTheDownload(t *testing.T) {
	src := readSource(t, "wsl.go")
	i := strings.Index(src, "dejimadInstallScript = `")
	if i < 0 {
		t.Fatal("dejimadInstallScript not found — renamed, and this guard checks nothing")
	}
	// Start AFTER the opening backtick. Searching from the marker itself finds
	// that same backtick immediately and yields an empty script — which then
	// contains none of the strings below and fails for entirely the wrong reason.
	// It did exactly that on the first run.
	script := src[i+len("dejimadInstallScript = `"):]
	if j := strings.Index(script, "`"); j > 0 {
		script = script[:j]
	}
	if strings.TrimSpace(script) == "" {
		t.Fatal("extracted an empty install script — the extraction is broken, so " +
			"this guard would fail (or pass) for a reason unrelated to the script")
	}

	// COMMENTS ARE STRIPPED FIRST, and this is not fussiness. The first version
	// of this guard checked the whole script, and the script's own comment
	// EXPLAINS the checksum step — so deleting the actual `sha256sum -c` left the
	// guard passing on the prose describing it. It survived its own mutation.
	//
	// wsl_shell_test.go records the identical failure happening three times in
	// one day on DEJIMA_ROLE. This was the fourth, written by someone who had
	// read that comment the same afternoon. Strip the comments.
	var code []string
	for _, line := range strings.Split(script, "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "#") {
			code = append(code, line)
		}
	}
	body := strings.Join(code, "\n")

	if !strings.Contains(body, "SHA256SUMS") {
		t.Error("the install script never fetches SHA256SUMS")
	}
	if !strings.Contains(body, "sha256sum -c") {
		t.Error("the install script does not verify the archive — the link this path " +
			"exists for drops connections mid-transfer, and an unverified truncated " +
			"tarball installs a corrupt daemon that fails later and elsewhere")
	}
}

// sudo must never be able to sit on a password prompt. This runs as a
// non-interactive child of `dejima wsl setup`; a prompt has no terminal to
// appear on, so the setup hangs with no output explaining why.
func TestWSLScriptsUseNonInteractiveSudo(t *testing.T) {
	src := readSource(t, "wsl.go")
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.Contains(trimmed, "sudo ") {
			continue
		}
		// Every sudo occurrence on the line must be the non-interactive form.
		for _, seg := range strings.Split(trimmed, "sudo ")[1:] {
			if !strings.HasPrefix(seg, "-n ") {
				t.Errorf("interactive sudo in an in-distro script: %q — a password "+
					"prompt here hangs setup with no output explaining why", trimmed)
				break
			}
		}
	}
}

// A failure inside the distro must carry what the distro SAID.
//
// wsl.Run returns the combined output alongside the error, and every install
// step used to discard it — so a failure surfaced as "install Docker engine:
// exit status 1", the one fact the operator already knew, while the apt error or
// DNS failure that explains it went into a variable nobody read. On the path
// with the worst network and the least ability to reproduce, every failure was
// invisible.
func TestWSLStepErrCarriesTheDistroOutput(t *testing.T) {
	base := errors.New("exit status 1")

	if got := wslStepErr("", nil); got != nil {
		t.Errorf("wslStepErr with no error returned %v, want nil", got)
	}
	// No output is not a reason to lose the error.
	if got := wslStepErr("", base); !errors.Is(got, base) {
		t.Errorf("the underlying error was dropped when output was empty: %v", got)
	}

	got := wslStepErr("E: Unable to locate package socat", base)
	if !errors.Is(got, base) {
		t.Error("the underlying error is no longer unwrappable — callers matching on " +
			"it would stop seeing it")
	}
	if !strings.Contains(got.Error(), "Unable to locate package socat") {
		t.Errorf("the distro's own words are missing: %v", got)
	}
}

// Tailed, not dumped. An image build log is thousands of lines, and printing all
// of it buries the last twenty that actually say what went wrong.
func TestWSLStepErrTailsLongOutput(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "noise line %d\n", i)
	}
	b.WriteString("the real error is here")

	got := wslStepErr(b.String(), errors.New("boom")).Error()
	if !strings.Contains(got, "the real error is here") {
		t.Error("the tail dropped the last line, which is the one that matters")
	}
	if strings.Contains(got, "noise line 0") {
		t.Error("the whole log was included — the useful lines are buried")
	}
	if n := strings.Count(got, "noise line"); n > 20 {
		t.Errorf("kept %d noise lines, want at most 20", n)
	}
}

// Every step that runs a script in the distro must route its failure through the
// helper. One that does not is invisible again, and it will be the one nobody
// tests because it only fails on someone else's machine.
func TestEveryWSLInstallStepReportsOutput(t *testing.T) {
	for _, fn := range []string{"installSocat", "installDocker", "installDejimad", "buildIslandImage"} {
		body := wslFuncBody(t, fn)
		if !strings.Contains(body, "wsl.Run(") {
			continue // not a step that shells into the distro
		}
		if !strings.Contains(body, "wslStepErr(") {
			t.Errorf("%s runs a script in the distro but does not report its output on "+
				"failure — the operator gets an exit status and nothing else", fn)
		}
	}
}
