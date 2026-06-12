// Package porttoken mints and resolves per-island bearer tokens for the
// authenticated in-island → dejimad path — the macOS autonomy route, where the
// unix socket can't be bind-mounted into a container so the brain must reach the
// daemon over loopback TCP (docs/port-island-spec.md §11.3, runbook §5).
//
// One token per island, stored host-side at ~/.dejima/projects/<name>/token
// (0600, outside every container's blast radius). A token authorizes actions
// scoped to *its* island only — the API middleware (a later slice) resolves the
// token to an island and rejects requests targeting any other. A Home Island
// additionally holds the tokens of the islands it spawns (the parent-child model
// — spawn returns the child's token — so there is no god-token).
package porttoken

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"os"
	"strings"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// Ensure returns the island's token, minting and persisting a fresh one if none
// exists yet. Idempotent.
func Ensure(island string) (string, error) {
	if tok, err := Load(island); err == nil && tok != "" {
		return tok, nil
	}
	tok, err := mint()
	if err != nil {
		return "", err
	}
	p, err := paths.TokenPath(island)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// Load returns the island's stored token, or "" if none is set.
func Load(island string) (string, error) {
	p, err := paths.TokenPath(island)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// IslandForToken returns the island a non-empty token authorizes, comparing
// against each island's stored token in constant time (no per-byte timing leak
// of the secret). Returns ok=false when no island matches.
func IslandForToken(token string) (island string, ok bool) {
	if token == "" {
		return "", false
	}
	projs, err := project.List()
	if err != nil {
		return "", false
	}
	for _, p := range projs {
		t, err := Load(p.Name)
		if err != nil || t == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return p.Name, true
		}
	}
	return "", false
}

// mint returns a fresh 256-bit hex token from crypto/rand.
func mint() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
