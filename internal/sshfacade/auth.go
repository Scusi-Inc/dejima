// Package sshfacade implements the daemon-side SSH front door for islands. The
// daemon (dejimad) is the single SSH endpoint for every island: it authenticates
// the connection against the target island's authorized_keys, then bridges the
// SSH session into the container via `docker exec` (see server.go). Islands run
// no sshd and expose no ports — the daemon brokers access exactly as it does for
// the websocket/PTY path, so this works identically on Linux and macOS and keeps
// containment intact. The SSH *username* selects the island; the public key
// authorizes it.
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

// Authorize reports whether offered is among the target island's authorized
// public keys. A missing authorized_keys file means "no keys authorized" → deny.
func Authorize(island string, offered ssh.PublicKey) (bool, error) {
	if err := project.ValidateName(island); err != nil {
		return false, err
	}
	keys, err := authorizedKeys(island)
	if err != nil {
		return false, err
	}
	want := offered.Marshal()
	for _, k := range keys {
		// Compare the wire-marshaled key bytes: exact key match, no parsing
		// ambiguity. bytes.Equal is fine — the key material is public.
		if bytes.Equal(k.Marshal(), want) {
			return true, nil
		}
	}
	return false, nil
}

// AddAuthorizedKey validates an "ssh-… AAAA… [comment]" line and appends it to
// the island's authorized_keys (deduped). Returns the parsed key's fingerprint.
func AddAuthorizedKey(island, line string) (string, error) {
	if err := project.ValidateName(island); err != nil {
		return "", err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", fmt.Errorf("not a valid authorized_keys line: %w", err)
	}
	existing, err := authorizedKeys(island)
	if err != nil {
		return "", err
	}
	for _, k := range existing {
		if bytes.Equal(k.Marshal(), pub.Marshal()) {
			return ssh.FingerprintSHA256(pub), nil // already present, idempotent
		}
	}
	path, err := paths.AuthorizedKeysPath(island)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(ssh.MarshalAuthorizedKey(pub)); err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(pub), nil
}

// authorizedKeys parses the island's authorized_keys file into public keys. A
// missing file yields an empty slice (no error) — an island with no registered
// keys simply rejects every connection.
func authorizedKeys(island string) ([]ssh.PublicKey, error) {
	path, err := paths.AuthorizedKeysPath(island)
	if err != nil {
		return nil, err
	}
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
