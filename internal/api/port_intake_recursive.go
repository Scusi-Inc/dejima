package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// Default bounds for a recursive intake. Both are overridable per request; both
// are checked before the first byte moves.
//
// The numbers are chosen to be obviously-fine for the intended use (a project
// directory, a folder of notes) and obviously-wrong for the mistake this guards
// against (pointing it at a home directory). A cap that only trips on genuinely
// enormous trees would let the mistake run for minutes first, which is the
// failure it exists to prevent.
const (
	defaultIntakeMaxFiles = 2000
	defaultIntakeMaxBytes = int64(512 << 20) // 512 MiB
)

// intakeCandidate is one regular file selected by the pre-pass walk.
type intakeCandidate struct {
	rel  string // slash-separated, relative to the imported directory
	size int64
}

// walkIntakeTree enumerates what a recursive intake WOULD copy, without copying
// anything. Separating discovery from transfer is what makes the caps meaningful:
// they are answered from the whole tree up front rather than discovered at file
// 400, so an over-large import fails in a second with a number in the message.
//
// SYMLINKS ARE NEVER FOLLOWED, and every one is reported. Be careful about which
// line you think is doing that work — there are three, and the explicit skip
// below is the LEAST important of them. Established by deleting each in turn:
//
//  1. resolveWithinScope, called per file in intakeOneFile, is the actual
//     security boundary. It EvalSymlinks-es the path and refuses anything landing
//     outside the scope root — a symlink to a file outside, and a directory
//     symlink, both rejected with "escapes the scope". This is the line that
//     stops a scope escape, and the transfer deliberately still goes through it
//     rather than trusting that the walk stayed inside.
//
//  2. The !IsRegular() check further down already keeps symlinks out of the
//     import: WalkDir does not follow them, so d.Info() reports the LINK, whose
//     mode is ModeSymlink and therefore not regular. Deleting the explicit skip
//     does NOT cause a symlink to be imported — confirmed by mutation.
//
//  3. The explicit skip changes only the REASON reported: "symlink (never
//     followed)" instead of "not a regular file". That is not cosmetic — a
//     directory of symlinks otherwise reads as an empty directory, and the
//     empty-import refusal would name the wrong cause — but it is not the
//     protection, and a future reader must not mistake it for one.
//
// It also keeps an expected condition off the error path, so "skipped 3 symlinks"
// reads as information rather than as three failures.
func walkIntakeTree(root string) (files []intakeCandidate, skipped []PortIntakeSkip, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable subdirectory is reported, not fatal: one bad directory
			// should not deny an import of everything beside it.
			rel, _ := filepath.Rel(root, p)
			skipped = append(skipped, PortIntakeSkip{
				Rel:    filepath.ToSlash(rel),
				Reason: "unreadable: " + walkErr.Error(),
			})
			return fs.SkipDir
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		relSlash := filepath.ToSlash(rel)

		// WalkDir does not follow symlinks, so d.Type() reports the LINK here
		// rather than its target. That is what makes this check reliable.
		if d.Type()&fs.ModeSymlink != 0 {
			skipped = append(skipped, PortIntakeSkip{Rel: relSlash, Reason: "symlink (never followed)"})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			skipped = append(skipped, PortIntakeSkip{Rel: relSlash, Reason: "unreadable: " + ierr.Error()})
			return nil
		}
		if !info.Mode().IsRegular() {
			// Sockets, FIFOs, devices. Copying them is meaningless at best and
			// blocks forever at worst.
			skipped = append(skipped, PortIntakeSkip{Rel: relSlash, Reason: "not a regular file"})
			return nil
		}
		files = append(files, intakeCandidate{rel: relSlash, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// Deterministic order: the Ledger reads as a sequence, and a stable one makes
	// two imports of the same tree comparable.
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Rel < skipped[j].Rel })
	return files, skipped, nil
}

// newBatchID returns a short random id grouping one import's Ledger entries.
func newBatchID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "batch"
	}
	return hex.EncodeToString(b[:])
}

// handlePortIntakeRecursive imports a directory as one brokered crossing per
// file. It is reached only from handlePortIntake, which has already resolved and
// scope-checked the directory and confirmed the island is running.
//
// Every file goes through the SAME per-file path a single intake uses — scope
// resolution, ledger-before-bytes, read-normalized staging. That is the whole
// design: a walk that calls the existing path inherits its properties, where a
// second transfer path would have to re-earn each one and is the obvious place
// for one to go missing.
func (s *Server) handlePortIntakeRecursive(
	w http.ResponseWriter, r *http.Request,
	p *project.Project, scope *project.PortScope,
	realRoot, relRoot, destRoot string,
) {
	req := intakeLimitsFrom(r)

	files, skipped, err := walkIntakeTree(realRoot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("walking %q: %w", relRoot, err))
		return
	}

	// "I copied nothing" and "there was nothing to copy" are different sentences.
	// Reporting success over an empty set is how an import that silently matched
	// no files gets mistaken for one that worked.
	if len(files) == 0 {
		msg := fmt.Sprintf("nothing to import: %q contains no regular files", relRoot)
		if len(skipped) > 0 {
			msg += fmt.Sprintf(" (%d entr%s skipped — %s)", len(skipped), plural(len(skipped), "y", "ies"), summarizeSkips(skipped))
		}
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s", msg))
		return
	}

	// Caps, before the first byte moves, with the actual numbers in the message —
	// a refusal that doesn't say how far over you are just prompts a guess.
	var total int64
	for _, f := range files {
		total += f.size
	}
	//
	// BOTH dimensions are reported whichever one tripped. They used to be two
	// separate refusals naming one number each, so raising max_files on a big
	// tree just bought you the max_bytes refusal on the next attempt — two round
	// trips over a tree the walk had already measured completely. The caller is
	// deciding whether to allow this import; give them the whole size of it.
	if over := capRefusal(relRoot, len(files), total, req); over != "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s", over))
		return
	}

	batch := newBatchID()
	resp := PortIntakeResponse{
		Scope: scope.Name, Src: realRoot, Dest: destRoot,
		Recursive: true, BatchID: batch, Skipped: skipped,
	}

	for i, f := range files {
		rel := pathpkg.Join(relRoot, f.rel)
		dest := destRoot + "/" + f.rel
		bytes, sum, err := s.intakeOneFile(r.Context(), p, scope, rel, dest,
			fmt.Sprintf("batch %s (%d/%d)", batch, i+1, len(files)))
		if err != nil {
			// No rollback. Un-copying files is a destructive operation invented to
			// tidy up a failure, and inventing destructive operations for that
			// reason is how work gets discarded. Report honestly instead: what
			// crossed stays crossed and stays ledgered.
			resp.Failed = append(resp.Failed, PortIntakeFailed{Rel: f.rel, Error: err.Error()})
			continue
		}
		resp.Files = append(resp.Files, PortIntakeFile{Rel: f.rel, Dest: dest, Bytes: bytes, SHA256: sum})
		resp.Bytes += bytes
	}

	// 207 rather than 200 when anything failed: the request did not succeed, and
	// a 200 with a failure list buried in it is exactly the "reports success while
	// something didn't happen" shape this repo keeps finding. Still 2xx, so the
	// body decodes and the caller can see precisely what crossed.
	status := http.StatusOK
	if len(resp.Failed) > 0 {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, resp)
}

// intakeOneFile performs a single brokered crossing: scope-resolve, hash, ledger
// (fail-closed), stage read-normalized, copy. This is the per-file path shared
// with the single-file intake — deliberately, so recursion cannot drift from it.
func (s *Server) intakeOneFile(
	ctx context.Context, p *project.Project, scope *project.PortScope,
	rel, dest, detail string,
) (int64, string, error) {
	// Re-resolved per file rather than trusting the walk's path. This is the check
	// that refuses a scope escape; skipping it because "the walk already stayed
	// inside" would move the security boundary into the walk, where it is much
	// easier to lose.
	real, _, err := resolveWithinScope(scope.HostPath, rel)
	if err != nil {
		return 0, "", err
	}
	size, sum, err := hashFile(real)
	if err != nil {
		return 0, "", err
	}
	// Fail closed: the crossing is recorded before any byte enters the island.
	if err := s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "trade.read", Island: p.Name, Scope: scope.Name, Path: rel,
		Mode: scope.Mode, Bytes: size, SHA256: sum, Decision: "allowed", Detail: detail,
	}); err != nil {
		return 0, "", fmt.Errorf("refusing to Trade: ledger write failed: %w", err)
	}
	staged, err := copyToTempReadable(real)
	if err != nil {
		return 0, "", err
	}
	defer os.Remove(staged)

	_, _, _, _ = s.rt.Exec(ctx, p.ContainerName(), []string{"mkdir", "-p", pathpkg.Dir(dest)})
	if err := s.rt.CopyToContainer(ctx, p.ContainerName(), staged, dest); err != nil {
		return 0, "", err
	}
	return size, sum, nil
}

// capRefusal returns the refusal text for an over-cap import, or "" to allow it.
//
// It names what the import ACTUALLY is on both axes and what both caps are, so
// one refusal is enough to decide with. The remedy names the request fields and
// the CLI flags together: the daemon cannot know which surface is asking, and a
// reader who only has one of the two names has to go looking for the other.
func capRefusal(relRoot string, count int, total int64, lim intakeLimits) string {
	if count <= lim.maxFiles && total <= lim.maxBytes {
		return ""
	}
	return fmt.Sprintf(
		"refusing to import %q: %d files, %s — the caps are %d files and %s. "+
			"Raise them for this import (max_files/max_bytes, or --max-files/--max-bytes), "+
			"or import a subdirectory.",
		relRoot, count, humanBytesInt64(total), lim.maxFiles, humanBytesInt64(lim.maxBytes))
}

// intakeLimits holds the resolved caps for one request.
type intakeLimits struct {
	maxFiles int
	maxBytes int64
}

// intakeLimitsFrom reads the caps off the decoded request stashed on the
// context by handlePortIntake, falling back to the defaults.
func intakeLimitsFrom(r *http.Request) intakeLimits {
	lim := intakeLimits{maxFiles: defaultIntakeMaxFiles, maxBytes: defaultIntakeMaxBytes}
	if v, ok := r.Context().Value(intakeLimitsKey{}).(intakeLimits); ok {
		if v.maxFiles > 0 {
			lim.maxFiles = v.maxFiles
		}
		if v.maxBytes > 0 {
			lim.maxBytes = v.maxBytes
		}
	}
	return lim
}

// intakeLimitsKey carries per-request caps from the decoded body to the walk
// without widening the handler signature further.
type intakeLimitsKey struct{}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// summarizeSkips renders up to three reasons so an empty-import refusal says
// WHY it found nothing — "contains no regular files" alone leaves a directory of
// symlinks looking like an empty directory.
func summarizeSkips(skipped []PortIntakeSkip) string {
	counts := map[string]int{}
	for _, s := range skipped {
		reason := s.Reason
		if i := strings.Index(reason, ":"); i > 0 {
			reason = reason[:i]
		}
		counts[reason]++
	}
	parts := make([]string, 0, len(counts))
	for reason, n := range counts {
		parts = append(parts, fmt.Sprintf("%d %s", n, reason))
	}
	sort.Strings(parts)
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, ", ")
}

// humanBytesInt64 renders a byte count for an error message.
func humanBytesInt64(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
