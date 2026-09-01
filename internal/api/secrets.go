package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/secrets"
)

// The per-island secrets API. Values go IN and are never returned: every
// response here carries secrets.Meta, which has no field for a value, so no
// handler can serialize one outward even by mistake.
//
// See docs/secrets-manager-spec.md.

// SecretsResponse lists an island's secrets as metadata.
type SecretsResponse struct {
	Secrets []secrets.Meta `json:"secrets"`
}

// PutSecretRequest sets or rotates one secret.
type PutSecretRequest struct {
	Value string `json:"value"`
}

// handleListSecrets returns an island's secret NAMES and metadata.
//
// Readable by any role, including an in-island token: knowing which secrets
// exist is useful to an agent and reveals nothing the environment wouldn't
// already — the values are in its own environment.
func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store, err := secrets.OpenIsland()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	metas, err := store.List(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if metas == nil {
		metas = []secrets.Meta{}
	}
	writeJSON(w, http.StatusOK, SecretsResponse{Secrets: metas})
}

// handlePutSecret sets or rotates a secret.
//
// Writes are refused for an in-island token. Not because it would expose the
// value — the agent can already read it from its own environment — but because
// an agent that can SET a secret can plant a value its peers will trust. That
// is the one boundary here that is real and enforceable.
func (s *Server) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	island, key := r.PathValue("name"), r.PathValue("key")
	if TokenIslandFromContext(r.Context()) != "" {
		writeError(w, http.StatusForbidden, errors.New(
			"an island token may not set secrets — an agent that can plant a value its peers "+
				"trust is an escalation path; ask the operator to set it"))
		return
	}
	var req PutSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	store, err := secrets.OpenIsland()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	meta, err := store.Set(island, key, req.Value, actorFromRequest(r))
	if err != nil {
		// A reserved or malformed name is the caller's mistake, not a server
		// fault, and the message explains which.
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Refresh the island's mount so a rotation takes effect without a recreate.
	// Best-effort: the secret IS stored, and a stale mount is a smaller problem
	// than failing a write that already succeeded.
	if refreshErr := s.refreshIslandSecrets(island); refreshErr != nil {
		s.log.Warn("island secrets mount not refreshed", "island", island, "err", refreshErr)
	}
	// Ledger the management event. The NAME and fingerprint, never the value —
	// an audit log that leaks the thing it audits would be worse than none.
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "secret.set", Island: island, Scope: key,
		Detail: "fingerprint " + meta.Fingerprint, Actor: actorFromRequest(r),
	})
	writeJSON(w, http.StatusOK, meta)
}

// handleDeleteSecret removes a secret. Same write gate as Put.
func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	island, key := r.PathValue("name"), r.PathValue("key")
	if TokenIslandFromContext(r.Context()) != "" {
		writeError(w, http.StatusForbidden, errors.New("an island token may not remove secrets"))
		return
	}
	store, err := secrets.OpenIsland()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := store.Remove(island, key); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if refreshErr := s.refreshIslandSecrets(island); refreshErr != nil {
		s.log.Warn("island secrets mount not refreshed", "island", island, "err", refreshErr)
	}
	s.ledgerAppend(ledger.ProvenanceBrokered, ledger.Entry{
		Type: "secret.remove", Island: island, Scope: key, Actor: actorFromRequest(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

// actorFromRequest names who performed a secret change, for the ledger. An
// island token is rejected before reaching here, so this is an operator.
func actorFromRequest(r *http.Request) string {
	if island := TokenIslandFromContext(r.Context()); island != "" {
		return "island:" + island
	}
	return "operator"
}

// refreshIslandSecrets rewrites the island's materialized secrets file so a
// running container picks the change up through its bind mount. A process
// already running keeps the environment it started with — callers surface that
// as a restart notice rather than pretending otherwise.
func (s *Server) refreshIslandSecrets(island string) error {
	store, err := secrets.OpenIsland()
	if err != nil {
		return err
	}
	_, err = materializeIslandSecrets(store, island)
	return err
}
