package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/srcscan"
)

// TestLocalCLITestsStayStubbed enforces the constraint stated at the top of
// local_cli_test.go, because a comment is not an enforcement — this repo has four instances
// in one week of a documented failure mode recurring, one of them written by
// someone who had read the write-up that afternoon.
//
// IT LIVES IN ITS OWN FILE, and that is not tidiness. The needle is a string
// literal, so a guard kept inside the file it scans matches its own code and
// fails on itself — which is the STRING form of the same bug, and it is how
// this test failed on the first run. A scanner must not scan itself; the
// coverage gate excludes itself from its corpus for the identical reason.
//
// It also strips comments first: scanning raw would match the warning
// paragraph in local_cli_test.go, which names cliEnv while forbidding it — a
// guard passing on prose about the thing rather than the thing.
func TestLocalCLITestsStayStubbed(t *testing.T) {
	path := filepath.Join(repoRoot(t), "cmd", "dejima", "local_cli_test.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src, ok := srcscan.StripGoComments(string(b))
	if !ok {
		t.Fatal("could not strip comments — scanning raw would match the warning itself")
	}
	// The control: prove we read THIS file and that the scan sees its code, so a
	// clean result cannot be an empty or wrong read.
	if !strings.Contains(src, "func stubDaemon(") {
		t.Fatalf("scanned %s but found no stubDaemon — wrong file, or the strip ate it", path)
	}
	if strings.Contains(src, "cliEnv(") {
		t.Fatal("a cliEnv test was added to local_cli_test.go. reachesTheServer is " +
			"FILE-scoped, so this qualifies the whole file and the stub route strings " +
			"below start crediting the local install/pull HANDLERS, which have no test " +
			"anywhere. Their waivers would then read as stale. Move the test to another file.")
	}
}
