package api

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/secrets"
)

// Materializing an island's secrets: the daemon writes a KEY=VALUE file on the
// host and bind-mounts it read-only into the island, mirroring how the GitHub
// identity reaches /opt/host/gh-config.
//
// The file is PARSED by the island, never sourced. A file sourced by bash
// executes what it reads, so a token containing a backtick or $(...) would be
// command injection into every new shell. Writing it in a fixed, unambiguous
// format is half of that guarantee; the in-island parser is the other half.
//
// A DIRECTORY is bind-mounted, not the file — and that distinction is the whole
// reason `dejima secret set` and `secret rm` used to report success while
// changing nothing the island could see.
//
// A file bind mount binds the INODE the path resolved to at container-create
// time. materializeIslandSecrets writes via CreateTemp + Rename, which puts a
// NEW inode at that path. So a container created against the file went on
// reading the ORIGINAL inode for its entire life: every later set and remove was
// invisible inside the island while the daemon reported success — the same
// says-contained-but-isn't shape as the grants pane. Mounting the directory
// makes the container resolve `secrets.env` on each access, so the rename is
// seen immediately and the atomic replace is kept.
//
// Only the mount subdirectory is exposed, not the island's whole secrets dir,
// which also holds meta.json. meta.json carries no values (names, timestamps
// and a sha256 fingerprint), so this is not plugging a leak — it is keeping the
// mount surface to exactly what is meant to cross, so a file added to that dir
// later doesn't silently become island-visible.

// secretsFileName is the materialized file. It lives in secretsMountDirName
// inside the island's secrets dir, so island teardown removes it along with
// everything else.
const secretsFileName = "secrets.env"

// secretsMountDirName is the ONLY thing bind-mounted into the island.
const secretsMountDirName = "mount"

// islandSecretsMountDir returns the host directory bind-mounted into the island
// WITHOUT creating anything.
func islandSecretsMountDir(island string) (string, error) {
	dir, err := paths.IslandSecretsPath(island)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, secretsMountDirName), nil
}

// islandSecretsFile returns the host path of an island's materialized secrets
// file WITHOUT creating anything.
func islandSecretsFile(island string) (string, error) {
	dir, err := islandSecretsMountDir(island)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, secretsFileName), nil
}

// encodeSecretsFile renders name/value pairs in the format the island parser
// expects: one `NAME=value` per line, values percent-escaped for the two bytes
// that would otherwise break line-based parsing.
//
// Escaping only \n and % (rather than shell-quoting) is deliberate. There is no
// quoting to get right because nothing evaluates this file — the parser splits
// on the first '=' and unescapes. A value may contain quotes, backticks,
// dollars, and spaces with no special handling, which is exactly the property
// that makes injection impossible rather than merely unlikely.
func encodeSecretsFile(vals map[string]string) string {
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names) // stable output: no spurious rewrites, readable diffs

	var b strings.Builder
	b.WriteString("# Written by dejimad. Do not edit — rewritten on every change.\n")
	b.WriteString("# Parsed, never sourced: values are opaque data, not shell.\n")
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteString(escapeSecretValue(vals[n]))
		b.WriteByte('\n')
	}
	return b.String()
}

// escapeSecretValue percent-escapes the only two bytes that matter to a
// line-based parser: the newline that would end the record early, and the
// escape character itself.
func escapeSecretValue(v string) string {
	v = strings.ReplaceAll(v, "%", "%25")
	v = strings.ReplaceAll(v, "\n", "%0A")
	return strings.ReplaceAll(v, "\r", "%0D")
}

// materializeIslandSecrets writes an island's secrets file and returns its host
// path. It ALWAYS writes the file — even with zero secrets, when the file is just
// the header comments — because the file is bind-mounted at container create and
// a mount can't be added to a live container. If the file were absent for an
// island that starts with no secrets, the FIRST secret added later would never
// reach it (the exact "my secret isn't showing up" bug). An empty (header-only)
// file injects nothing, so deleting the last secret is still honored: new shells
// export no values. (A process already running keeps its start-time environment
// until it restarts — callers surface that as a restart notice.)
func materializeIslandSecrets(store *secrets.IslandStore, island string) (string, error) {
	path, err := islandSecretsFile(island)
	if err != nil {
		return "", err
	}
	vals, err := store.Values(island)
	if err != nil {
		return "", err
	}

	// Ensure the 0700 dirs exist (this is a write path, unlike reads).
	if _, err := paths.IslandSecretsDir(island); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	// Write 0600 via a temp file in the same directory, so the mount never
	// observes a half-written file and the value is never briefly world-readable.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secrets-*.env")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(encodeSecretsFile(vals)); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("write island secrets: %w", err)
	}
	return path, nil
}

// islandSecretsMount returns the host DIRECTORY to bind at secretsMountPath,
// refreshing the file inside it first so a container start always carries
// current values. The directory (and thus the mount) is ALWAYS present —
// secrets.env is header-only when the island has no secrets yet — so a secret
// added later reaches the running container through the live mount instead of
// needing a recreate to gain the mount.
func islandSecretsMount(p *project.Project) (string, error) {
	store, err := secrets.OpenIsland()
	if err != nil {
		return "", err
	}
	file, err := materializeIslandSecrets(store, p.Name)
	if err != nil {
		return "", err
	}
	if file == "" {
		return "", nil
	}
	return filepath.Dir(file), nil
}
