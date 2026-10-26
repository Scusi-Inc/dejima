package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/selfupdate"
)

// The generated drop-in must (a) pin both binary destinations and the restart
// target, with the wildcard only in the copy's source position, and (b) actually
// parse as sudoers — a malformed drop-in can wedge sudo for the whole machine.
func TestSudoersTemplate(t *testing.T) {
	const bin = "/usr/local/bin"
	content := fmt.Sprintf(sudoersTemplate, "alice",
		selfupdate.StagingPath(bin+"/dejima"), selfupdate.StagingPath(bin+"/dejimad"),
		bin+"/dejima", bin+"/dejimad", launchdLabel)

	for _, want := range []string{
		"alice ALL=(root) NOPASSWD:",
		"/usr/bin/install -m 0755 " + selfupdate.StagingPath(bin+"/dejima") + " " + bin + "/dejima,",
		"/usr/bin/install -m 0755 " + selfupdate.StagingPath(bin+"/dejimad") + " " + bin + "/dejimad,",
		"/bin/launchctl kickstart -k system/" + launchdLabel,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered sudoers missing %q\n---\n%s", want, content)
		}
	}
	// No wildcard anywhere now: sudo 1.9.17 rejects one in an argument, and the
	// source is pinned to the staging path instead. See sudoers_drift_test.go.
	if strings.Contains(content, "*") {
		t.Errorf("wildcard must not appear at all\n%s", content)
	}

	visudo := "/usr/sbin/visudo"
	if _, err := os.Stat(visudo); err != nil {
		t.Skipf("visudo unavailable (%v); content checks passed", err)
	}
	f := filepath.Join(t.TempDir(), "dejima")
	if err := os.WriteFile(f, []byte(content), 0o440); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(visudo, "-cf", f).CombinedOutput(); err != nil {
		t.Fatalf("visudo rejected generated sudoers: %v\n%s", err, out)
	}
}
