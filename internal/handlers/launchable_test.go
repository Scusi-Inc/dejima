package handlers

import (
	"os/exec"
	"strings"
	"testing"
)

// A bundled handler's launch command must be one its binary actually accepts.
//
// `codex --sandbox-policy=no-sandbox` was not. Codex has `--sandbox`, and no
// `no-sandbox` value; the real flag exits 2 with "unexpected argument". So the
// tmux session died the instant it started, and attaching dropped the operator
// on a bare shell prompt with nothing on screen suggesting an agent had been
// meant to run — reported as "I had to type codex myself".
//
// It had been wrong since this registry was written. Nothing checked it, because
// the launch string is data: it compiles, it round-trips through every test that
// reads the registry, and it is only ever wrong at the moment a container starts.
//
// So this EXECUTES the flags against the real binary, which means it can only
// run where that binary exists — every island, and any dev machine with the
// agent installed, but not a bare CI runner.
//
// EACH BINARY GETS A POSITIVE CONTROL FIRST. `--help` is only a useful oracle
// against a parser that rejects unknown flags before printing help. Codex (clap)
// does; `claude --not-a-flag --help` exits 0, so for that binary the check cannot
// distinguish a good launch command from a bad one. Without the control this
// guard reported a pass for claude-code while being structurally incapable of
// failing — which is the exact shape of the bug it exists to catch, one level up.
func TestBundledLaunchCommandsAreAcceptedByTheirBinaries(t *testing.T) {
	const bogus = "--dejima-not-a-real-flag"
	validated := 0

	for id, h := range registry {
		if !h.Bundled || h.Launch == "" {
			continue
		}
		fields := strings.Fields(h.Launch)
		bin := fields[0]
		if _, err := exec.LookPath(bin); err != nil {
			t.Logf("%s: %s is not installed here, so nothing to check", id, bin)
			continue
		}

		t.Run(id, func(t *testing.T) {
			// Control: does this binary reject a flag that certainly does not exist?
			if err := exec.Command(bin, bogus, "--help").Run(); err == nil {
				t.Skipf("%s accepts %q, so `--help` cannot tell a valid launch "+
					"command from an invalid one — this binary is unverifiable by "+
					"this method and is NOT being checked", bin, bogus)
			}

			args := append(append([]string{}, fields[1:]...), "--help")
			if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
				t.Errorf("`%s` is rejected by the installed %s: %v\n%s\n\n"+
					"An island launching this agent dies immediately and the operator "+
					"lands on a shell prompt.", h.Launch, bin, err, out)
			}
			validated++
		})
	}

	if validated == 0 {
		t.Skip("no bundled agent binary here could be verified: either none are " +
			"installed, or none reject unknown flags. This guard checked nothing.")
	}
}
