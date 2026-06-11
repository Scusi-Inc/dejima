package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// handleAudit returns the brokered-operation Ledger and verifies its hash chain.
// ?limit=N tails the last N entries (the full chain is still verified).
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	lg, err := ledger.Default()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	entries, err := lg.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp := AuditResponse{Total: len(entries), Verified: true}
	if verr := lg.Verify(); verr != nil {
		resp.Verified = false
		resp.Error = verr.Error()
	}
	if n := r.URL.Query().Get("limit"); n != "" {
		if lim, e := strconv.Atoi(n); e == nil && lim >= 0 && lim < len(entries) {
			entries = entries[len(entries)-lim:]
		}
	}
	resp.Entries = entries
	writeJSON(w, http.StatusOK, resp)
}

// handleListPortScopes returns the brokered host-file grants for an island.
func (s *Server) handleListPortScopes(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, PortScopesResponse{Scopes: scopeViews(p.Ports)})
}

// handleGrantPortScope grants an island read-only brokered access to a host
// directory. The host path is validated on the daemon host (the broker's
// vantage point); the grant is persisted and ledgered.
func (s *Server) handleGrantPortScope(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req PortScopeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = project.PortModeRO
	}
	switch mode {
	case project.PortModeRO:
		// ok
	case project.PortModeRW:
		writeError(w, http.StatusBadRequest, errPortRWUnsupported)
		return
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid mode %q (want %q)", mode, project.PortModeRO))
		return
	}
	if !filepath.IsAbs(req.HostPath) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("host path must be absolute, got %q", req.HostPath))
		return
	}
	hp := filepath.Clean(req.HostPath)
	info, err := os.Stat(hp)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("host path %q is not reachable on the daemon host: %w", hp, err))
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, fmt.Errorf("host path %q must be a directory", hp))
		return
	}
	scope, err := p.AddPortScope(project.PortScope{HostPath: hp, Mode: mode, GrantedAt: time.Now().UTC()})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "port.grant", Island: name, Scope: scope.Name,
		Path: scope.HostPath, Mode: scope.Mode, Decision: "allowed",
	})
	writeJSON(w, http.StatusCreated, scopeView(scope))
}

// handleRevokePortScope drops a grant by its scope name.
func (s *Server) handleRevokePortScope(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	scopeName := r.PathValue("scope")
	removed, ok := p.RemovePortScope(scopeName)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no Port scope %q", name, scopeName))
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "port.revoke", Island: name, Scope: removed.Name,
		Path: removed.HostPath, Mode: removed.Mode, Decision: "allowed",
	})
	w.WriteHeader(http.StatusNoContent)
}

// ledgerAppend writes one entry to the process-wide ledger. Returns the error
// so callers that must fail-closed (file Trades) can abort when the audit
// record can't be written; scope grant/revoke treat it as best-effort.
func (s *Server) ledgerAppend(e ledger.Entry) error {
	lg, err := ledger.Default()
	if err != nil {
		s.log.Error("ledger unavailable", "err", err)
		return err
	}
	if _, err := lg.Append(e); err != nil {
		s.log.Error("ledger append failed", "type", e.Type, "island", e.Island, "err", err)
		return err
	}
	return nil
}

func scopeView(s project.PortScope) PortScopeView {
	return PortScopeView{Name: s.Name, HostPath: s.HostPath, Mode: s.Mode, GrantedAt: s.GrantedAt}
}

func scopeViews(ss []project.PortScope) []PortScopeView {
	out := make([]PortScopeView, 0, len(ss))
	for _, s := range ss {
		out = append(out, scopeView(s))
	}
	return out
}

var errPortRWUnsupported = staticErr("read-write Port scopes are not supported yet; V1 is read-only (docs/port-island-spec.md §6). Use :ro")
