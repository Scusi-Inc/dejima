package main

import (
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
