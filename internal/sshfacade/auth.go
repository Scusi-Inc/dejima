// Package sshfacade implements the daemon-side SSH front door for islands. The
// daemon (dejimad) is the single SSH endpoint for every island: it authenticates
// the connection against the target island's authorized_keys, then bridges the
// SSH session into the container via `docker exec` (see server.go). Islands run
// no sshd and expose no ports — the daemon brokers access exactly as it does for
// the websocket/PTY path, so this works identically on Linux and macOS and keeps
// containment intact. The SSH *username* selects the island; the public key
// authorizes it.
//
// Two key stores authorize a connection (their union):
//   - per-island authorized_keys (~/.dejima/projects/<name>/ssh/authorized_keys)
//     — grant a specific key access to one island (e.g. a collaborator).
//   - the account allow-set (~/.dejima/ssh_authorized_keys) — keys that may ssh
//     into *every* island, current and future. Register once (your laptop, your
//     desktop) and all islands accept it with no per-island step.
package sshfacade

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"

	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// HostSigner returns the daemon's SSH host key, generating and persisting a
// fresh ed25519 key (0600) on first use. One host key for the daemon — it is the
// single SSH front door, so clients pin one identity regardless of island.
func HostSigner() (ssh.Signer, error) {
	p, err := paths.HostKeyPath()
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(p)
	if err == nil {
		signer, perr := ssh.ParsePrivateKey(keyPEM)
		if perr != nil {
			return nil, fmt.Errorf("parse host key %s: %w", p, perr)
		}
		return signer, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "dejimad")
	if err != nil {
		return nil, fmt.Errorf("marshal host key: %w", err)
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, fmt.Errorf("write host key %s: %w", p, err)
	}
	return ssh.NewSignerFromSigner(priv)
}

// Fingerprint returns the SHA256 fingerprint of a signer's public key, for the
// CLI to display so clients can pin the daemon's host key.
func Fingerprint(signer ssh.Signer) string {
	return ssh.FingerprintSHA256(signer.PublicKey())
}

// Authorize reports whether offered is allowed to ssh into island — true if the
// key is in the island's own authorized_keys OR in the account-wide allow-set. A
// missing file in either store simply contributes no keys. The island name is
// validated first (defense against a traversal in the SSH username).
func Authorize(island string, offered ssh.PublicKey) (bool, error) {
	if err := project.ValidateName(island); err != nil {
		return false, err
	}
	islandPath, err := paths.AuthorizedKeysPath(island)
	if err != nil {
		return false, err
	}
	acctPath, err := paths.AccountAuthorizedKeysPath()
	if err != nil {
		return false, err
	}
	want := offered.Marshal()
	for _, path := range []string{islandPath, acctPath} {
		keys, err := parseKeysFromFile(path)
		if err != nil {
			return false, err
		}
		for _, k := range keys {
			// Compare wire-marshaled bytes: exact key match, no parsing
			// ambiguity. bytes.Equal is fine — the key material is public.
			if bytes.Equal(k.Marshal(), want) {
				return true, nil
			}
		}
	}
	return false, nil
}

// --- per-island keys --------------------------------------------------------

// AddAuthorizedKey validates an "ssh-… AAAA… [comment]" line and appends it to
// the island's authorized_keys (deduped). Returns the parsed key's fingerprint.
func AddAuthorizedKey(island, line string) (string, error) {
	if err := project.ValidateName(island); err != nil {
		return "", err
	}
	path, err := paths.AuthorizedKeysPath(island)
	if err != nil {
		return "", err
	}
	return addKeyToFile(path, line)
}

// ListAuthorizedKeys returns the island's authorized keys in file order.
func ListAuthorizedKeys(island string) ([]KeyInfo, error) {
	if err := project.ValidateName(island); err != nil {
		return nil, err
	}
	path, err := paths.AuthorizedKeysPath(island)
	if err != nil {
		return nil, err
	}
	return listKeysInFile(path)
}

// RemoveAuthorizedKey deletes the island key whose SHA256 fingerprint matches.
// Returns how many were removed (0 if no match).
func RemoveAuthorizedKey(island, fingerprint string) (int, error) {
	if err := project.ValidateName(island); err != nil {
		return 0, err
	}
	path, err := paths.AuthorizedKeysPath(island)
	if err != nil {
		return 0, err
	}
	return removeKeyFromFile(path, func(pub ssh.PublicKey) bool {
		return ssh.FingerprintSHA256(pub) == fingerprint
	})
}

// RemoveAllAuthorizedKeys clears every authorized key for the island.
func RemoveAllAuthorizedKeys(island string) (int, error) {
	if err := project.ValidateName(island); err != nil {
		return 0, err
	}
	path, err := paths.AuthorizedKeysPath(island)
	if err != nil {
		return 0, err
	}
	return removeKeyFromFile(path, func(ssh.PublicKey) bool { return true })
}

// --- account-wide keys ------------------------------------------------------

// AddAccountKey appends a key to the account-wide allow-set — keys that may ssh
// into every island, current and future. Returns the key's fingerprint.
func AddAccountKey(line string) (string, error) {
	path, err := paths.AccountAuthorizedKeysPath()
	if err != nil {
		return "", err
	}
	return addKeyToFile(path, line)
}

// ListAccountKeys returns the account-wide keys in file order.
func ListAccountKeys() ([]KeyInfo, error) {
	path, err := paths.AccountAuthorizedKeysPath()
	if err != nil {
		return nil, err
	}
	return listKeysInFile(path)
}

// RemoveAccountKey deletes the account key whose SHA256 fingerprint matches.
// Revoking here instantly removes fleet-wide access (no per-island cleanup).
func RemoveAccountKey(fingerprint string) (int, error) {
	path, err := paths.AccountAuthorizedKeysPath()
	if err != nil {
		return 0, err
	}
	return removeKeyFromFile(path, func(pub ssh.PublicKey) bool {
		return ssh.FingerprintSHA256(pub) == fingerprint
	})
}

// RemoveAllAccountKeys clears the account-wide allow-set.
func RemoveAllAccountKeys() (int, error) {
	path, err := paths.AccountAuthorizedKeysPath()
	if err != nil {
		return 0, err
	}
	return removeKeyFromFile(path, func(ssh.PublicKey) bool { return true })
}

// --- shared file helpers ----------------------------------------------------

// KeyInfo is one authorized public key, for display.
type KeyInfo struct {
	Fingerprint string // SHA256:…
	Type        string // e.g. ssh-ed25519
	Comment     string
}

// marshalKeyLine renders an authorized_keys line, keeping the comment so keys
// stay identifiable in `dejima ssh list`.
func marshalKeyLine(pub ssh.PublicKey, comment string) []byte {
	line := bytes.TrimRight(ssh.MarshalAuthorizedKey(pub), "\n")
	if comment != "" {
		line = append(line, ' ')
		line = append(line, comment...)
	}
	return append(line, '\n')
}

// addKeyToFile validates an authorized_keys line and appends it to path
// (deduped, 0600). Returns the parsed key's fingerprint.
func addKeyToFile(path, line string) (string, error) {
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", fmt.Errorf("not a valid authorized_keys line: %w", err)
	}
	existing, err := parseKeysFromFile(path)
	if err != nil {
		return "", err
	}
	for _, k := range existing {
		if bytes.Equal(k.Marshal(), pub.Marshal()) {
			return ssh.FingerprintSHA256(pub), nil // already present, idempotent
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(marshalKeyLine(pub, comment)); err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(pub), nil
}

// listKeysInFile parses path into display records. A missing file yields an
// empty slice (no error).
func listKeysInFile(path string) ([]KeyInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var infos []KeyInfo
	rest := data
	for len(bytes.TrimSpace(rest)) > 0 {
		pub, comment, _, remainder, perr := ssh.ParseAuthorizedKey(rest)
		if perr != nil {
			return infos, fmt.Errorf("parse %s: %w", path, perr)
		}
		infos = append(infos, KeyInfo{Fingerprint: ssh.FingerprintSHA256(pub), Type: pub.Type(), Comment: comment})
		rest = remainder
	}
	return infos, nil
}

// removeKeyFromFile rewrites path keeping only keys for which drop returns false
// (comments preserved). No-op (and no write) when nothing matches, so a typo'd
// fingerprint can't silently truncate the file. Returns how many were removed.
func removeKeyFromFile(path string, drop func(ssh.PublicKey) bool) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var kept bytes.Buffer
	removed := 0
	rest := data
	for len(bytes.TrimSpace(rest)) > 0 {
		pub, comment, _, remainder, perr := ssh.ParseAuthorizedKey(rest)
		if perr != nil {
			return 0, fmt.Errorf("parse %s: %w", path, perr)
		}
		if drop(pub) {
			removed++
		} else {
			kept.Write(marshalKeyLine(pub, comment))
		}
		rest = remainder
	}
	if removed == 0 {
		return 0, nil
	}
	if err := os.WriteFile(path, kept.Bytes(), 0o600); err != nil {
		return 0, err
	}
	return removed, nil
}

// parseKeysFromFile parses path into public keys. A missing file yields an empty
// slice (no error) — it simply contributes no authorized keys.
func parseKeysFromFile(path string) ([]ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var keys []ssh.PublicKey
	rest := data
	for len(bytes.TrimSpace(rest)) > 0 {
		pub, _, _, remainder, perr := ssh.ParseAuthorizedKey(rest)
		if perr != nil {
			// Stop at the first unparseable line rather than silently trusting a
			// truncated file; a corrupt entry shouldn't make us skip later keys
			// without notice.
			return keys, fmt.Errorf("parse %s: %w", path, perr)
		}
		keys = append(keys, pub)
		rest = remainder
	}
	return keys, nil
}
