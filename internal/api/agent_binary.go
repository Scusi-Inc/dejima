package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
)

// Does the agent binary this island PROMISED to preinstall actually run?
//
// `tmux new-session` succeeds whether or not the command it is given can
// execute. tmux forks, the command dies, tmux reaps the window, and the daemon —
// which only ever saw exit code 0 from tmux itself — records a healthy agent.
// The operator attaches and gets:
//
//	dejima@e1c6fd58e4c0:/workspace$
//
// A bare prompt, with nothing on screen suggesting an agent had been meant to
// run. This has now been reported twice from agent CREATION, for two unrelated
// causes: a launch flag the binary rejected (`--sandbox-policy=no-sandbox`,
// recorded in handlers/launchable_test.go), and a codex whose platform binary
// was missing because `npm install -g` treats a failed OPTIONAL dependency as
// non-fatal:
//
//	Error: Missing optional dependency @openai/codex-linux-arm64.
//
// image/Dockerfile asserts `codex --version` at build time and would stop both.
// It landed 2026-09-02; the island that hit this was built 2026-08-20, and
// NOTHING REBUILDS AN ISLAND THAT ALREADY EXISTS. A guard that only runs at
// build time cannot reach the containers already on disk, and its own comment
// says why the obvious remedy does not work either: "Creating the agent cannot
// fix it, because the agent uses this image rather than installing anything."
//
// So the check belongs HERE, in the daemon, which is the one component that both
// reaches existing islands and is present at the moment the operator is
// watching. Putting it in image/start.sh would repeat the exact mistake — that
// file is COPYed into the image too.

// binaryProbeBudget bounds the probe. `--version` on a working agent returns in
// milliseconds; this is only ever spent on a binary that is already wedged.
var binaryProbeBudget = 10 * time.Second

// verifyPreinstalledBinary reports that a bundled agent's launch binary cannot
// run in this island, or nil when there is nothing to say.
//
// NIL IS ALSO THE ANSWER WHEN THE PROBE ITSELF FAILS TO RUN. A probe that cannot
// reach the container (engine hiccup, container mid-stop, exec refused) knows
// nothing about the binary, and turning that ignorance into a refusal would let
// a flake block a working agent. Only a probe that RAN and came back non-zero is
// evidence, which is the difference between "the binary is broken" and "I could
// not look".
func (s *Server) verifyPreinstalledBinary(ctx context.Context, p *project.Project, a *project.AgentSpec) error {
	h, ok := handlers.Lookup(a.Type)
	if !ok {
		return nil
	}
	bin := h.PreinstalledBinary()
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, binaryProbeBudget)
	defer cancel()

	// `bash -lc` so PATH matches what the launch will actually see — the agent
	// binaries live in /opt/dejima/npm-global/bin, which a bare exec's minimal
	// PATH does not include.
	//
	// `--version` is the probe because it is the one both bundled agents support
	// and the SAME assertion image/Dockerfile makes at build time. It answers the
	// question that matters: not "is a file at this path" (it is — the npm
	// wrapper installs fine, it is the platform binary underneath that is
	// missing) but "does running this produce a live process".
	_, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{
		"bash", "-lc", bin + " --version",
	})
	if err != nil {
		s.log.Debug("agent binary probe did not run", "island", p.Name, "agent", a.ID, "err", err)
		return nil
	}
	if code == 0 {
		return nil
	}
	return fmt.Errorf("the %s binary in this island cannot run (`%s --version` exited %d)%s\n"+
		"This island's image is stale or its %s install is broken. Rebuild the image and roll "+
		"islands onto it:\n"+
		"  dejima image build && dejima upgrade %s\n"+
		"Re-creating the agent will not help — the agent uses the image rather than installing anything",
		bin, bin, code, firstLine(stderr), bin, p.Name)
}

// firstLine returns the most informative line of a probe's stderr, prefixed for
// the message above, or "" when there is nothing worth quoting.
//
// The first line, not the whole thing: a broken node wrapper prints a stack
// trace, and the operator needs the sentence, not the frames.
func firstLine(stderr string) string {
	for _, ln := range strings.Split(stderr, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			return ": " + ln
		}
	}
	return ""
}
