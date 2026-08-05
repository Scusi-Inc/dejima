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
// Bind mounts reflect host writes live, so rotating a secret updates the island
// without recreating the container — though a process already running keeps the
// environment it started with, which is why callers surface a restart notice.

// secretsFileName is the materialized file, inside the island's secrets dir so
// island teardown removes it along with everything else.
const secretsFileName = "secrets.env"

// islandSecretsFile returns the host path of an island's materialized secrets
// file WITHOUT creating anything.
func islandSecretsFile(island string) (string, error) {
	dir, err := paths.IslandSecretsPath(island)
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

	// Ensure the 0700 dir exists (this is a write path, unlike reads).
	if _, err := paths.IslandSecretsDir(island); err != nil {
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

// islandSecretsMount returns the host path to bind at /opt/host/secrets.env,
// refreshing the file first so a container start always carries current values.
// The file (and thus the mount) is ALWAYS present — header-only when the island
// has no secrets yet — so a secret added later reaches the running container
// through the live mount instead of needing a recreate to gain the mount.
func islandSecretsMount(p *project.Project) (string, error) {
	store, err := secrets.OpenIsland()
	if err != nil {
		return "", err
	}
	return materializeIslandSecrets(store, p.Name)
}
