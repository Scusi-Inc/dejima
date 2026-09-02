package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The socat step is run FOR REAL under /bin/sh against stub package managers, on
// the two distro shapes that matter. Asserting on the script's text would only
// restate it; running it is what distinguishes "reads fine" from "works".
//
// Field case: a WSL distro running as ROOT with no sudo installed. The step led
// with `sudo -n apt-get`, so it died with "sudo: not found" on a machine that
// needed no sudo at all — and the client then reported the daemon as
// unreachable, because socat IS the tunnel.
func runSocatScript(t *testing.T, withSudo bool, aptNeedsRoot bool) (string, error) {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")

	// apt-get: when aptNeedsRoot, it refuses unless invoked through the sudo
	// stub (which marks the environment). That models a non-root distro.
	apt := "#!/bin/sh\necho \"apt-get $*\" >> " + log + "\n"
	if aptNeedsRoot {
		apt += "[ \"$STUB_ELEVATED\" = 1 ] || { echo 'permission denied' >&2; exit 1; }\n"
	}
	apt += "exit 0\n"
	write(t, filepath.Join(bin, "apt-get"), apt)

	if withSudo {
		write(t, filepath.Join(bin, "sudo"),
			"#!/bin/sh\n[ \"$1\" = -n ] && shift\nSTUB_ELEVATED=1 export STUB_ELEVATED\nexec \"$@\"\n")
	}
	write(t, filepath.Join(bin, "command"), "#!/bin/sh\nexit 0\n") // unused; `command` is a builtin

	cmd := exec.Command("/bin/sh", "-c", socatInstallScript)
	cmd.Env = []string{"PATH=" + bin, "HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	body, _ := os.ReadFile(log)
	return string(out) + "\n--calls--\n" + string(body), err
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A root distro with NO sudo. This is the reported failure.
func TestSocatInstallsAsRootWithoutSudo(t *testing.T) {
	out, err := runSocatScript(t, false, false)
	if err != nil {
		t.Fatalf("socat install failed on a root distro with no sudo — the exact field case:\n%s\nerr: %v", out, err)
	}
	if !strings.Contains(out, "apt-get install") {
		t.Errorf("never actually invoked the package manager:\n%s", out)
	}
}

// A non-root distro where apt needs elevation: the sudo fallback must still run.
func TestSocatFallsBackToSudoWhenNotRoot(t *testing.T) {
	out, err := runSocatScript(t, true, true)
	if err != nil {
		t.Fatalf("socat install failed where sudo was required and available:\n%s\nerr: %v", out, err)
	}
	if !strings.Contains(out, "apt-get install") {
		t.Errorf("never reached the install through sudo:\n%s", out)
	}
}
