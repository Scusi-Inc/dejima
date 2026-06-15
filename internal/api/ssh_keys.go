package api

// ssh_keys.go serves the SSH account-key endpoints — the daemon-side of device
// self-enrollment (`dejima ssh enroll` / TUI 'S'). Any operator device can
// register its own public key over the API instead of copy-pasting it onto the
// daemon host, and the DAEMON performs the file write — so the key always lands
// where the daemon actually reads authorized_keys from (no user-vs-root
// ~/.dejima ownership mismatch on a system service).
//
// These are operator routes: absent from tokenRouteAccess, so the in-island
// token path (tokenauth.go, default-deny) can never reach them. A contained
// agent cannot authorize an SSH key into any island.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aoos/dejima/internal/sshfacade"
)

func (s *Server) handleAuthorizeAccountKey(w http.ResponseWriter, r *http.Request) {
	var req AuthorizeSSHKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid request body"))
		return
	}
	if req.PublicKey == "" {
		writeError(w, http.StatusBadRequest, errors.New("public_key is required"))
		return
	}
	fp, err := sshfacade.AddAccountKey(req.PublicKey)
	if err != nil {
		// AddAccountKey validates the key line; a parse failure is a client error.
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.log.Info("ssh account key enrolled", "fingerprint", fp)
	writeJSON(w, http.StatusOK, AuthorizeSSHKeyResponse{Fingerprint: fp})
}

func (s *Server) handleListAccountKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := sshfacade.ListAccountKeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := ListSSHKeysResponse{Keys: make([]SSHKeyInfo, 0, len(keys))}
	for _, k := range keys {
		out.Keys = append(out.Keys, SSHKeyInfo{Fingerprint: k.Fingerprint, Type: k.Type, Comment: k.Comment})
	}
	writeJSON(w, http.StatusOK, out)
}
