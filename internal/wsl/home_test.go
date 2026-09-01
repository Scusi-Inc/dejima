package wsl

import (
	"os/exec"
	"strings"
	"testing"
)

// Every in-distro script must run with $HOME set.
//
// `wsl.exe -d <distro> -- sh -c …` does not pass HOME, and /bin/sh on Ubuntu is
// dash, which — unlike bash — does not synthesise it from the passwd entry. So
// HOME was the empty string in every script here, and it surfaced as
//
//	mkdir: cannot create directory '': No such file or directory
//
// naming neither HOME nor the shell.
//
// It hid for so long because the scripts that use $HOME had never successfully
// run, and the one path that DID work went through `curl … | bash` — bash fills
// HOME in. The same distro therefore looked fine from one command and broken
// from another, which is why "it worked when I did it by hand" was true and
// misleading at the same time.
//
// This matters past a work directory: dejimad derives its socket and config from
// HOME, so a daemon started with it empty writes to /.dejima while the client
// looks elsewhere — a far quieter failure than this one.
func TestHomePreambleSetsHomeUnderDash(t *testing.T) {
	if _, err := exec.LookPath("dash"); err != nil {
		t.Skip("dash not installed here; CI covers it")
	}
	// env -u HOME reproduces the distro: HOME genuinely absent, not empty-string.
	cmd := exec.Command("dash", "-c", homePreamble+`printf '%s' "$HOME"`)
	cmd.Env = []string{"PATH=/usr/bin:/bin"} // no HOME
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("preamble failed under dash: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got == "" {
		t.Fatal("HOME is still empty after the preamble — every script using $HOME " +
			"builds a path starting at nothing, and dejimad would put its socket in " +
			"/.dejima while the client looks somewhere else")
	}
	if !strings.HasPrefix(got, "/") {
		t.Errorf("HOME = %q, which is not an absolute path", got)
	}
}

// An already-set HOME must be left alone. The preamble runs before every script,
// so overriding a real HOME would be worse than not setting one.
func TestHomePreambleDoesNotOverrideAnExistingHome(t *testing.T) {
	if _, err := exec.LookPath("dash"); err != nil {
		t.Skip("dash not installed here; CI covers it")
	}
	cmd := exec.Command("dash", "-c", homePreamble+`printf '%s' "$HOME"`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/home/someone"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("preamble failed: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "/home/someone" {
		t.Errorf("HOME = %q, want the value already in the environment", got)
	}
}

// The preamble is prepended to every script, so it must not disturb what follows
// — no output of its own, and a clean exit status.
func TestHomePreambleIsSilent(t *testing.T) {
	if _, err := exec.LookPath("dash"); err != nil {
		t.Skip("dash not installed here; CI covers it")
	}
	cmd := exec.Command("dash", "-c", homePreamble+`printf 'PAYLOAD'`)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("preamble broke the script: %v", err)
	}
	// `uname -m` is read and parsed by the caller; anything the preamble prints
	// would be taken for the architecture.
	if got := string(out); got != "PAYLOAD" {
		t.Errorf("output = %q, want exactly %q — the preamble is contributing text "+
			"that a caller parsing this output would read as its result", got, "PAYLOAD")
	}
}
