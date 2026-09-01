package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// SetIslandIdentityRequest is the body of PUT /v1/islands/{name}/identity: the
// operator-chosen color + glyph override. Both fields are required and validated
// server-side (hex color, single-rune glyph).
type SetIslandIdentityRequest struct {
	Color string `json:"color"`
	Glyph string `json:"glyph"`
}

// hexColor matches a CSS-style hex color: #rgb or #rrggbb (case-insensitive).
var hexColor = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// validateColor enforces a hex color of the form #rgb or #rrggbb.
func validateColor(color string) error {
	if !hexColor.MatchString(color) {
		return fmt.Errorf("color %q must be a hex string (#rgb or #rrggbb)", color)
	}
	return nil
}

// validateGlyph enforces a glyph of exactly one rune.
func validateGlyph(glyph string) error {
	if utf8.RuneCountInString(glyph) != 1 {
		return fmt.Errorf("glyph %q must be exactly one character", glyph)
	}
	return nil
}

// setIslandIdentity sets an island's operator-chosen visual identity (color +
// glyph) override, persisted in config.toml and reflected back in
// IslandInfo.Identity. Operator-only: absent from tokenRouteAccess, so an
// in-island (contained) token is default-denied — an island can never set its
// own identity. Scope is strictly color + glyph; this is not theming.
func (s *Server) setIslandIdentity(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req SetIslandIdentityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	color := strings.TrimSpace(req.Color)
	glyph := req.Glyph // a glyph may legitimately be whitespace-adjacent; the rune-count check is the gate
	if err := validateColor(color); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateGlyph(glyph); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p.Identity = project.Identity{Color: color, Glyph: glyph}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "identity.set", Island: name,
		Detail: fmt.Sprintf("color=%s glyph=%s", color, glyph), Decision: "allowed",
	})
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}

// clearIslandIdentity removes an island's visual-identity override, returning it
// to the TUI's deterministic per-name default. Operator-only (see
// setIslandIdentity). Idempotent: clearing an already-unset identity is a no-op
// that still returns the (unchanged) island info.
func (s *Server) clearIslandIdentity(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	p.Identity = project.Identity{} // zero it
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "identity.clear", Island: name, Decision: "allowed",
	})
	writeJSON(w, http.StatusOK, s.toInfo(r.Context(), p))
}
