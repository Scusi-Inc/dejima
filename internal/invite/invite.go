// Package invite encodes and decodes Dejima team invites — a single paste-safe
// blob that carries everything a teammate needs to connect to a daemon
// (host:port + bearer secret + role/scope). The owner issues one in the TUI Team
// view at token-mint time; the teammate pastes it on the join side and is
// connected with no env vars or manual flags.
//
// Wire format: "dejima-invite:" + base64url(no-pad) of the JSON Payload. The
// base64url alphabet (RFC 4648 §5) is URL/filename-safe, so the blob survives
// copy/paste, chat, QR, and shell args without "+", "/", or "=" being mangled.
//
// The blob is ENCODED, not encrypted: it contains the bearer secret in the
// clear (post-base64). It is therefore a credential — treat it like a password,
// send it over a trusted channel, and revoke the underlying token to kill a
// leaked invite (DELETE /v1/tokens/{id}).
package invite

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Scheme prefixes every invite blob; it makes an invite recognisable to a paste
// handler and greppable in logs/chat.
const Scheme = "dejima-invite:"

// Version is the current payload schema version. Decode rejects anything else,
// so an older client fails loudly on a newer invite instead of misreading it.
const Version = 1

// Payload is the decoded invite. Keys are short to keep the blob compact (a
// typical invite is ~120–180 chars). Host/Token/Role are required; the rest are
// display/UX hints.
type Payload struct {
	V       int      `json:"v"`
	Host    string   `json:"host"`              // daemon host:port the teammate dials (operator-supplied)
	Token   string   `json:"token"`             // bearer secret (the credential)
	Role    string   `json:"role"`              // owner|operator|viewer (echo; daemon enforces real scope)
	Islands []string `json:"islands,omitempty"` // scope echo; empty = all islands
	Name    string   `json:"name,omitempty"`    // suggested profile name
	Label   string   `json:"label,omitempty"`   // token label / who it's for (display)
}

var validRoles = map[string]bool{"owner": true, "operator": true, "viewer": true}

// validate checks the required fields and value domains shared by Encode and
// Decode, so a malformed Payload can never be produced or accepted.
func (p Payload) validate() error {
	if strings.TrimSpace(p.Host) == "" {
		return errors.New("invite: host is required")
	}
	if strings.TrimSpace(p.Token) == "" {
		return errors.New("invite: token is required")
	}
	if !validRoles[p.Role] {
		return fmt.Errorf("invite: role %q must be one of owner, operator, viewer", p.Role)
	}
	return nil
}

// Encode validates p, stamps the current Version, and returns the paste-safe
// blob. Run it at token-mint time — the bearer secret is only available once.
func Encode(p Payload) (string, error) {
	p.V = Version
	if err := p.validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("invite: marshal: %w", err)
	}
	return Scheme + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode is the strict, total inverse of Encode: every malformed input yields a
// clear error (never a panic or a partial Payload), so the join side can render
// the message verbatim. Surrounding whitespace (from a sloppy paste) is trimmed.
func Decode(s string) (Payload, error) {
	var p Payload
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, Scheme) {
		return p, fmt.Errorf("invite: not a Dejima invite (missing %q prefix)", Scheme)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, Scheme))
	if err != nil {
		return p, fmt.Errorf("invite: corrupt invite (bad base64): %w", err)
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("invite: corrupt invite (bad JSON): %w", err)
	}
	if p.V != Version {
		return Payload{}, fmt.Errorf("invite: unsupported version %d (this build understands v%d) — update Dejima", p.V, Version)
	}
	if err := p.validate(); err != nil {
		return Payload{}, err
	}
	return p, nil
}
