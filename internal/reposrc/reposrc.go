// Package reposrc discovers git repositories on the client filesystem and
// resolves a user-supplied --repo value into something the daemon can clone.
//
// Resolution is deliberately client-side: the TUI and CLI run where the repos
// live, while the daemon may run on another host (DEJIMA_HOST). The default is
// to hand the daemon a *remote URL* (works regardless of where the daemon
// runs); a read-only local-copy seed is only used when the daemon is local and
// the repo is local-only or carries unpushed work.
package reposrc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo is a git repository discovered on disk.
type Repo struct {
	Path   string // absolute path on the client
	Name   string // base name, used as the default island name
	Origin string // origin remote URL, or "" if none
}

// Status is the (lazily computed) working-tree state of a repo.
type Status struct {
	Dirty  bool // uncommitted or untracked changes
	Ahead  int  // commits ahead of upstream
	Behind int  // commits behind upstream
}

// Resolution is the outcome of turning a --repo value into a create request.
type Resolution struct {
	Repo     string // value to send as CreateIslandRequest.Repo (URL or upstream)
	SeedPath string // host path to bind-mount read-only as a clone source, or ""
	Mode     string // "remote" or "local-copy"
	Note     string // human-readable explanation of what will happen
}

// IsURL reports whether s looks like a git URL rather than a local path. It
// recognizes scheme URLs (https://, ssh://, git://) and scp-like SSH shorthand
// (git@github.com:owner/repo).
func IsURL(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	if i := strings.Index(s, ":"); i > 0 {
		host := s[:i]
		if strings.Contains(host, "@") && !strings.Contains(host, "/") {
			return true
		}
	}
	return false
}

// expandHome resolves a leading ~ to the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// OriginURL reads the origin remote URL straight from <path>/.git/config without
// spawning git. Returns "" (no error) when there is no origin.
func OriginURL(path string) (string, error) {
	f, err := os.Open(filepath.Join(path, ".git", "config"))
	if err != nil {
		return "", err
	}
	defer f.Close()

	inOrigin := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if inOrigin {
			if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "url" {
				return strings.TrimSpace(v), nil
			}
		}
	}
	return "", sc.Err()
}

// Discover walks root up to maxDepth levels deep and returns every git repo it
// finds, reading each origin from .git/config. It does not descend into a repo
// once found, and never spawns git — it is cheap enough to run on every load.
func Discover(root string, maxDepth int) ([]Repo, error) {
	root = expandHome(root)
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var out []Repo
	rootDepth := strings.Count(abs, string(filepath.Separator))

	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs rather than abort the whole scan
		}
		if !d.IsDir() {
			return nil
		}
		if path != abs {
			base := d.Name()
			if strings.HasPrefix(base, ".") { // skip dotdirs (incl. .git itself)
				return filepath.SkipDir
			}
		}
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
			origin, _ := OriginURL(path)
			out = append(out, Repo{Path: path, Name: filepath.Base(path), Origin: origin})
			return filepath.SkipDir // don't descend into a repo
		}
		if strings.Count(path, string(filepath.Separator))-rootDepth >= maxDepth {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetStatus computes the working-tree status for a repo. It shells out to git,
// so callers should run it lazily (e.g. for the highlighted repo only), not for
// a whole list on every render. Errors collapse to a zero Status.
func GetStatus(path string) Status {
	var st Status
	if out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output(); err == nil {
		st.Dirty = len(strings.TrimSpace(string(out))) > 0
	}
	// Ahead/behind vs the configured upstream; silently zero if none is set.
	if out, err := exec.Command("git", "-C", path, "rev-list", "--count", "--left-right", "@{upstream}...HEAD").Output(); err == nil {
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) == 2 {
			fmt.Sscanf(fields[0], "%d", &st.Behind)
			fmt.Sscanf(fields[1], "%d", &st.Ahead)
		}
	}
	return st
}

// Resolve turns a --repo value into a Resolution. daemonLocal must be true only
// when the daemon shares this filesystem (no DEJIMA_HOST). forceLocalCopy forces
// the read-only seed path even when an origin exists (to capture unpushed work).
func Resolve(input string, daemonLocal, forceLocalCopy bool) (Resolution, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Resolution{}, fmt.Errorf("empty repo")
	}
	if IsURL(input) {
		return Resolution{Repo: input, Mode: "remote", Note: "cloning from " + input}, nil
	}

	abs, err := filepath.Abs(expandHome(input))
	if err != nil {
		return Resolution{}, err
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return Resolution{}, fmt.Errorf("%s is not a git repository", input)
	}
	origin, _ := OriginURL(abs)

	if !forceLocalCopy && origin != "" {
		// Default: clone from the canonical remote. Works whether the daemon is
		// local or remote, and origin is already correct inside the island.
		return Resolution{Repo: origin, Mode: "remote", Note: "cloning from origin (" + origin + ")"}, nil
	}

	// Local-copy requested, or there is no remote to clone from.
	if !daemonLocal {
		if origin != "" {
			return Resolution{Repo: origin, Mode: "remote",
				Note: "remote daemon can't read local files; cloning from origin instead"}, nil
		}
		return Resolution{}, fmt.Errorf(
			"%s has no git remote and the daemon is remote (DEJIMA_HOST is set); push it to a remote first, or run against a local daemon",
			input)
	}
	note := "seeding from local copy at " + abs
	if origin != "" {
		note += "; origin will point at " + origin
	} else {
		note += "; no remote (origin will be unset)"
	}
	return Resolution{Repo: origin, SeedPath: abs, Mode: "local-copy", Note: note}, nil
}
