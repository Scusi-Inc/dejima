package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of --client: it must NEVER contact a daemon. A laptop or
// Windows box that only drives a remote server has no local daemon, so the full
// uninstall's "is the daemon running?" is exactly what this avoids. It only
// removes this machine's connection config and points at removing the binary.
func TestUninstallClientTouchesNoDaemonAndRemovesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".dejima")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(dir, "client.json")
	host := filepath.Join(dir, "host.json")
	for _, p := range []string{client, host} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// yes=true so it runs without a prompt. If this reached a daemon it would
	// hang or error on a socket that doesn't exist — the test's temp HOME has no
	// daemon and no socket.
	if err := uninstallClient(true); err != nil {
		t.Fatalf("uninstallClient errored (did it try to reach a daemon?): %v", err)
	}

	for _, p := range []string{client, host} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived --client uninstall", filepath.Base(p))
		}
	}
}

// Running on a machine that was never configured must not error — a bare CLI
// install with no saved connection is a normal case.
func TestUninstallClientOnUnconfiguredMachine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := uninstallClient(true); err != nil {
		t.Errorf("--client on an unconfigured machine should be a clean no-op, got: %v", err)
	}
}

// On Windows the installer writes three things this command cannot remove, and
// the old output claimed "nothing else on this machine is Dejima's" — which was
// false, and sent at least one operator away believing the CLI was gone while a
// working dejima.exe sat on their PATH. Each leftover must be named.
func TestWindowsLeftoversNameEveryUnremovedArtifact(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\test\AppData\Local`)
	t.Setenv("DEJIMA_PREFIX", "")

	out := captureStdout(t, printWindowsLeftovers)

	for _, want := range []string{
		`C:\Users\test\AppData\Local\dejima`, // the program dir no package manager owns
		"Path",                               // the User PATH entry
		"DEJIMA_HOST",                        // outranks every saved profile
		"Get-Command dejima -All",            // how to confirm it actually went
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Windows uninstall output never mentions %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Nothing else on this machine is Dejima's") {
		t.Error("the all-clear must not be printed on Windows — three artifacts survive")
	}
}

// A custom install prefix has to be honoured, or the command confidently tells
// the operator to delete a directory they never installed into.
func TestWindowsLeftoversHonoursCustomPrefix(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\test\AppData\Local`)
	t.Setenv("DEJIMA_PREFIX", `D:\tools\dejima`)

	out := captureStdout(t, printWindowsLeftovers)

	if !strings.Contains(out, `D:\tools\dejima`) {
		t.Errorf("custom DEJIMA_PREFIX ignored:\n%s", out)
	}
	if strings.Contains(out, `AppData\Local\dejima`) {
		t.Errorf("named the default prefix despite DEJIMA_PREFIX being set:\n%s", out)
	}
}

// The Windows defect, arriving on Unix later: install-client.sh appends a PATH
// line to a shell startup file, and the all-clear was about to be false in the
// same way it had been on Windows.
func TestShellPathLeftoverIsNamedWhenPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	rc := filepath.Join(home, ".zshrc")
	body := "alias ll='ls -l'\n\n# added by the dejima installer\nexport PATH=\"$HOME/.local/bin:$PATH\"\n"
	if err := os.WriteFile(rc, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { printShellPathLeftovers() })

	if !strings.Contains(out, rc) {
		t.Errorf("the rc file holding the installer's PATH line was not named:\n%s", out)
	}
	if strings.Contains(out, "Nothing else on this machine") {
		t.Error("the all-clear must not appear when a leftover was found")
	}
}

// The other half, and the one that keeps the all-clear honest: a machine whose
// PATH already contained the install dir never got an rc edit, so there is
// genuinely nothing left and the command must still say so.
func TestNoShellLeftoverReportsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// An rc file exists but carries no Dejima line.
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("alias ll='ls -l'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var found bool
	out := captureStdout(t, func() { found = printShellPathLeftovers() })

	if found {
		t.Error("an rc file with no installer marker must not count as a leftover")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("nothing should be printed when there is nothing to report:\n%s", out)
	}
}
