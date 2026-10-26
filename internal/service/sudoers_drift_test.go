package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/selfupdate"
)

// The sudoers rule and the command selfupdate actually runs must spell the same
// source path. sudo matches a command line positionally and literally, so a
// divergence does not degrade — the grant stops applying, and a headless daemon
// fails with "a password is required" pointing nowhere near the cause.
//
// This is the check that makes StagingPath load-bearing rather than decorative:
// it fails if either side stops using it.
func TestSudoersSourceMatchesWhatSelfUpdateRuns(t *testing.T) {
	const bin = "/usr/local/bin"
	content := fmt.Sprintf(sudoersTemplate, "alice",
		selfupdate.StagingPath(bin+"/dejima"), selfupdate.StagingPath(bin+"/dejimad"),
		bin+"/dejima", bin+"/dejimad", launchdLabel)

	for _, target := range []string{bin + "/dejima", bin + "/dejimad"} {
		// Built from the SAME function the updater execs, so this cannot drift
		// into asserting a command nothing runs.
		want := strings.Join(selfupdate.ElevatedInstallArgv(target), " ")
		if !strings.Contains(content, want) {
			t.Errorf("the rule does not authorise the command self-update runs:\nwant: %s\n---\n%s",
				want, content)
		}
	}
}

// No wildcards anywhere in the command section. sudo 1.9.17 refuses to parse
// one in an argument, and the previous rule had two — which is why this file
// exists.
func TestSudoersCarriesNoWildcards(t *testing.T) {
	content := fmt.Sprintf(sudoersTemplate, "alice",
		selfupdate.StagingPath("/usr/local/bin/dejima"), selfupdate.StagingPath("/usr/local/bin/dejimad"),
		"/usr/local/bin/dejima", "/usr/local/bin/dejimad", launchdLabel)
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "*") {
			t.Errorf("a wildcard survives in a command line, which current sudo "+
				"rejects outright: %q", line)
		}
	}
}
