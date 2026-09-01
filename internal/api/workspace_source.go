package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// Seeding /workspace from a host directory that isn't a repo.
//
// THIS IS A WRAPPER, NOT A MECHANISM, and the distinction is the whole design.
// It grants a Port scope, runs the SAME per-file ledgered crossing the recursive
// intake uses, and drops the scope. It owns no copy loop of its own.
//
// A create-time path that copied host files directly would be a second,
// unaudited door into an island — precisely the divergence folder import exists
// to close, reintroduced at a different entrance. If this file ever grows its
// own transfer code, that has gone wrong.
//
// The grant is also the audit trail: `port.grant` names the directory, one
// `trade.read` per file records what crossed, and `port.revoke` closes it. An
// operator reading the Ledger later can see exactly how those files arrived.

// folderSeed carries the create-time request to seed from a host directory.
// Passed as one struct rather than three more positional parameters — provision
// already takes twelve, and the next reader should not have to count commas to
// see which bool is which.
type folderSeed struct {
	Dir       string
	KeepScope bool
	GitInit   bool
}

// seedWorkspaceFromDir copies a host directory into the island's /workspace
// through the Port broker.
//
// SYNCHRONOUS, deliberately. The alternative is an async seed with a readiness
// state machine, which means a window where the island exists and /workspace is
// half-populated — and `workspaceReady` short-circuits to ready for a repo-less
// island, so that window would report READY while files were still arriving. The
// caps bound the wait instead; a folder too large for them is refused up front
// with the two-step path named, rather than turning create into something that
// looks hung.
func (s *Server) seedWorkspaceFromDir(ctx context.Context, p *project.Project, seed folderSeed) error {
	hostDir := filepath.Clean(seed.Dir)
	info, err := os.Stat(hostDir)
	if err != nil {
		return fmt.Errorf("source folder %q is not reachable on the daemon host: %w", hostDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory (a single file is `dejima port intake`)", hostDir)
	}

	// Enumerate before granting anything: an over-cap folder should be refused
	// without having created a scope it then has to clean up, and refusing after
	// a grant leaves the operator with standing host access they never asked for.
	files, skipped, err := walkIntakeTree(hostDir)
	if err != nil {
		return fmt.Errorf("reading %q: %w", hostDir, err)
	}
	if len(files) == 0 {
		msg := fmt.Sprintf("nothing to copy: %q contains no regular files", hostDir)
		if len(skipped) > 0 {
			msg += fmt.Sprintf(" (%d skipped — %s)", len(skipped), summarizeSkips(skipped))
		}
		return fmt.Errorf("%s", msg)
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	// Named alternative, not just a limit. A refusal that only says "too big"
	// leaves the operator with no next step; the two-step path has no create
	// timeout over it and handles any size.
	if len(files) > defaultIntakeMaxFiles {
		return fmt.Errorf("refusing to seed %d files from %q at create time: the limit is %d — "+
			"create the island with --no-repo, then `dejima port grant` + `dejima port intake -r`, "+
			"which has no create timeout over it",
			len(files), hostDir, defaultIntakeMaxFiles)
	}
	if total > defaultIntakeMaxBytes {
		return fmt.Errorf("refusing to seed %s from %q at create time: the limit is %s — "+
			"create the island with --no-repo, then `dejima port grant` + `dejima port intake -r`, "+
			"which has no create timeout over it",
			humanBytesInt64(total), hostDir, humanBytesInt64(defaultIntakeMaxBytes))
	}

	scope, err := p.AddPortScope(project.PortScope{
		HostPath: hostDir, Mode: project.PortModeRO, GrantedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("granting a scope for %q: %w", hostDir, err)
	}
	if err := p.Save(); err != nil {
		return err
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "port.grant", Island: p.Name, Scope: scope.Name,
		Path: scope.HostPath, Mode: scope.Mode, Decision: "allowed",
		Detail: "granted to seed /workspace at create",
	})

	// The scope comes back off unless it was asked for. It was needed to copy,
	// not to keep — leaving it would hand the island standing read access to the
	// operator's directory as a side effect of a create flag.
	defer func() {
		if seed.KeepScope {
			return
		}
		if _, ok := p.RemovePortScope(scope.Name); !ok {
			return
		}
		if err := p.Save(); err != nil {
			s.log.Error("dropping the seed scope", "island", p.Name, "err", err)
			return
		}
		s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
			Type: "port.revoke", Island: p.Name, Scope: scope.Name,
			Path: scope.HostPath, Decision: "allowed",
			Detail: "seed complete; scope was create-time only",
		})
	}()

	batch := newBatchID()
	var copied int
	for i, f := range files {
		dest := "/workspace/" + f.rel
		if _, _, err := s.intakeOneFile(ctx, p, &scope, f.rel, dest,
			fmt.Sprintf("seed %s (%d/%d)", batch, i+1, len(files))); err != nil {
			// Stop at the first failure and keep both the island and what crossed.
			//
			// Note what is NOT the reason: the copied files are not precious. This
			// is a COPY — the source folder on the host is untouched, so nothing
			// here is irrecoverable and "destroying work" would be the wrong
			// argument. The reason is narrower and practical: the island itself is
			// fine (name, agents, volumes), so tearing it down would make the
			// operator recreate all of that to retry a copy they can simply re-run
			// with `port intake -r`.
			return fmt.Errorf("seeding /workspace failed at %q after %s: %w "+
				"(what already copied is still there, and is in the Ledger under batch %s)",
				f.rel, countNounAPI(copied, "file"), err, batch)
		}
		copied++
	}

	if seed.GitInit {
		// Only on an explicit ask. The consequences are named at the flag, not
		// here, because by this point the decision has already been made.
		if _, stderr, code, err := s.rt.Exec(ctx, p.ContainerName(),
			[]string{"git", "-C", "/workspace", "init", "-q"}); err != nil || code != 0 {
			return fmt.Errorf("copied %s, but `git init` failed: %v %s "+
				"(the files are there; run git init yourself)", countNounAPI(copied, "file"), err, stderr)
		}
	}
	return nil
}

// countNounAPI renders "1 file" / "3 files" for daemon-side messages.
func countNounAPI(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
