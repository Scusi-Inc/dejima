package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoos/dejima/internal/api"
)

// `dejima cp -r` is the CONVENIENT path, and convenient here means UNAUDITED.
//
// It copies straight through the daemon's file endpoints and writes NO Ledger
// entries. `dejima port intake -r` is the audited path: scoped, and one ledgered
// crossing per file. Both exist on purpose — the distinction between them is the
// product, not an implementation detail — so the help text says so plainly
// rather than leaving someone to discover it during an audit.
//
// It exists at all because the alternative is worse. Without it people reach for
// tar + cp + untar over exec, which is also unaudited, and additionally leaves a
// temp archive lying around and fails in ways nobody reports.

// Caps, shared with the brokered path. The spec mandates them for intake only,
// but "point it at a home directory" is the same mistake with the same remedy,
// and a copy that silently starts moving 40k files is not better for being
// unaudited.
const (
	cpMaxFiles = 2000
	cpMaxBytes = int64(512 << 20)
)

// cpEntry is one file selected for a recursive copy.
type cpEntry struct {
	rel  string // slash-separated, relative to the source root
	size int64
}

// cpSkip records something deliberately not copied.
type cpSkip struct {
	rel    string
	reason string
}

// walkLocalTree enumerates regular files under root, never following symlinks.
//
// Deliberately NOT shared with the daemon's walkIntakeTree despite the near
// identical rules. That one runs inside the security perimeter and pairs with
// resolveWithinScope, which is the check that actually refuses a scope escape;
// this one has no scope at all and copies whatever the caller can already read.
// Sharing the code would imply they share that property, and the next person to
// change one would reasonably assume the other was covered.
func walkLocalTree(root string) (files []cpEntry, skipped []cpSkip, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			rel, _ := filepath.Rel(root, p)
			skipped = append(skipped, cpSkip{filepath.ToSlash(rel), "unreadable: " + walkErr.Error()})
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
		// WalkDir does not follow symlinks, so this reports the LINK. Copying
		// through one would silently duplicate content, or leave the destination
		// holding a copy of something the user only meant to reference.
		if d.Type()&fs.ModeSymlink != 0 {
			skipped = append(skipped, cpSkip{relSlash, "symlink (never followed)"})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			skipped = append(skipped, cpSkip{relSlash, "unreadable: " + ierr.Error()})
			return nil
		}
		if !info.Mode().IsRegular() {
			skipped = append(skipped, cpSkip{relSlash, "not a regular file"})
			return nil
		}
		files = append(files, cpEntry{relSlash, info.Size()})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].rel < skipped[j].rel })
	return files, skipped, nil
}

// checkCpCaps refuses an over-large copy before the first byte moves, naming the
// actual numbers — a refusal that does not say how far over you are just prompts
// a guess and a retry.
func checkCpCaps(files []cpEntry, what string) error {
	if len(files) > cpMaxFiles {
		return fmt.Errorf("refusing to copy %d files from %s: the limit is %d (copy a subdirectory)",
			len(files), what, cpMaxFiles)
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	if total > cpMaxBytes {
		return fmt.Errorf("refusing to copy %s from %s: the limit is %s (copy a subdirectory)",
			humanBytes(uint64(total)), what, humanBytes(uint64(cpMaxBytes)))
	}
	return nil
}

// cpResult accumulates one recursive copy so the caller can report honestly.
type cpResult struct {
	copied  int
	bytes   int64
	skipped []cpSkip
	failed  []cpSkip // reason holds the error
}

// report prints the outcome and returns a non-nil error when anything failed.
// A partial copy must not exit 0: files that landed are real and are NOT removed
// (un-copying to tidy up a failure is how work gets destroyed), but reporting
// success over an incomplete result is the thing this avoids.
func (r cpResult) report(src, dst string) error {
	fmt.Printf("copied %s (%s) %s → %s\n", countNoun(r.copied, "file"), humanBytes(uint64(r.bytes)), src, dst)
	for _, s := range r.skipped {
		fmt.Printf("  skipped %s — %s\n", s.rel, s.reason)
	}
	if len(r.failed) == 0 {
		return nil
	}
	for _, f := range r.failed {
		fmt.Fprintf(os.Stderr, "  FAILED %s — %s\n", f.rel, f.reason)
	}
	return fmt.Errorf("%s did not copy (%s did); nothing was removed",
		countNoun(len(r.failed), "file"), countNoun(r.copied, "file"))
}

// cpToIslandRecursive walks a local directory and writes each file into the
// island. Parent directories are created by the daemon's write handler, so
// nested paths need no special handling here.
func cpToIslandRecursive(ctx context.Context, c *api.Client, srcDir, island, dstDir string) error {
	files, skipped, err := walkLocalTree(srcDir)
	if err != nil {
		return fmt.Errorf("walking %s: %w", srcDir, err)
	}
	if len(files) == 0 {
		return emptyCopyError(srcDir, skipped)
	}
	if err := checkCpCaps(files, srcDir); err != nil {
		return err
	}
	res := cpResult{skipped: skipped}
	for _, f := range files {
		dst := pathpkg.Join(dstDir, f.rel)
		if err := cpOneToIsland(ctx, c, filepath.Join(srcDir, filepath.FromSlash(f.rel)), island, dst); err != nil {
			res.failed = append(res.failed, cpSkip{f.rel, err.Error()})
			continue
		}
		res.copied++
		res.bytes += f.size
	}
	return res.report(srcDir, island+":"+dstDir)
}

func cpOneToIsland(ctx context.Context, c *api.Client, src, island, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return c.WriteFile(ctx, island, dst, in)
}

// cpFromIslandRecursive enumerates regular files inside the island and streams
// each one out.
//
// `find -type f` matches regular files only, so symlinks are excluded by the
// same rule the local walk applies — a symlink is -type l. -print0 rather than
// newline separation because a filename may legally contain a newline, and
// splitting on one would silently truncate the list, producing a copy that
// looks complete and is not.
func cpFromIslandRecursive(ctx context.Context, c *api.Client, island, srcDir, dstDir string) error {
	out, err := c.ExecInIsland(ctx, island, []string{"find", srcDir, "-type", "f", "-print0"})
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("listing %s in %s failed: %s", srcDir, island, strings.TrimSpace(out.Stderr))
	}
	var rels []string
	for _, p := range strings.Split(out.Stdout, "\x00") {
		if p == "" {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, strings.TrimSuffix(srcDir, "/")), "/")
		if rel != "" {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	if len(rels) == 0 {
		return fmt.Errorf("nothing to copy: %s in %s contains no regular files "+
			"(symlinks and directories are not copied)", srcDir, island)
	}
	if len(rels) > cpMaxFiles {
		return fmt.Errorf("refusing to copy %d files from %s:%s: the limit is %d (copy a subdirectory)",
			len(rels), island, srcDir, cpMaxFiles)
	}

	var res cpResult
	for _, rel := range rels {
		n, err := cpOneFromIsland(ctx, c, island, pathpkg.Join(srcDir, rel), filepath.Join(dstDir, filepath.FromSlash(rel)))
		if err != nil {
			res.failed = append(res.failed, cpSkip{rel, err.Error()})
			continue
		}
		res.copied++
		res.bytes += n
	}
	return res.report(island+":"+srcDir, dstDir)
}

func cpOneFromIsland(ctx context.Context, c *api.Client, island, src, dst string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	rc, err := c.ReadFile(ctx, island, src)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, rc)
}

// emptyCopyError distinguishes "I copied nothing" from "there was nothing to
// copy". A directory of only symlinks is indistinguishable from an empty one
// once they are skipped, so the refusal has to say which happened.
func emptyCopyError(what string, skipped []cpSkip) error {
	if len(skipped) == 0 {
		return fmt.Errorf("nothing to copy: %s contains no regular files", what)
	}
	reasons := map[string]int{}
	for _, s := range skipped {
		r := s.reason
		if i := strings.Index(r, ":"); i > 0 {
			r = r[:i]
		}
		reasons[r]++
	}
	parts := make([]string, 0, len(reasons))
	for r, n := range reasons {
		parts = append(parts, fmt.Sprintf("%d %s", n, r))
	}
	sort.Strings(parts)
	return fmt.Errorf("nothing to copy: %s contains no regular files (%s skipped)",
		what, strings.Join(parts, ", "))
}
