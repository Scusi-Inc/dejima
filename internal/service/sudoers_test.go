package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The generated drop-in must (a) pin both binary destinations and the restart
// target, with the wildcard only in the copy's source position, and (b) actually
// parse as sudoers — a malformed drop-in can wedge sudo for the whole machine.
func TestSudoersTemplate(t *testing.T) {
	content := fmt.Sprintf(sudoersTemplate,
		"alice", "/usr/local/bin/dejima", "/usr/local/bin/dejimad", launchdLabel)

	for _, want := range []string{
		"alice ALL=(root) NOPASSWD:",
		"/usr/bin/install -m 0755 * /usr/local/bin/dejima,",
		"/usr/bin/install -m 0755 * /usr/local/bin/dejimad,",
		"/bin/launchctl kickstart -k system/" + launchdLabel,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered sudoers missing %q\n---\n%s", want, content)
		}
	}
	// The wildcard belongs only to the source of the copy, never the destination.
	if strings.Contains(content, "* /usr/local/bin/dejima ") || strings.Contains(content, "/usr/local/bin/*") {
		t.Errorf("wildcard must not reach a destination path\n%s", content)
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
