package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/aoos/dejima/internal/capability"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/project"
)

// Bounds on a capability invocation's argument map — keep the wire + ledger
// small and the adapter input sane. String values only (enforced by the type).
const (
	maxCapArgs     = 32
	maxCapKeyLen   = 64
	maxCapValueLen = 4096
)

// capability.go is the host-side capability broker's grant surface (Phase 1):
// grant / revoke / list the named host actions an island may invoke. Execution
// (POST /v1/capabilities/execute) and the adapters land in later phases; until
// then a grant is a recorded, ledgered permission with no runtime effect.
//
// Grants are operator-only — these routes are not in tokenRouteAccess, so the
// in-island token path can never reach them (a contained brain can never grant
// itself a capability), exactly as with Port scope grants.

// handleListCapabilityGrants returns the capability targets an island may invoke.
func (s *Server) handleListCapabilityGrants(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, CapabilityGrantsResponse{Grants: capabilityViews(p.Capabilities)})
}

// handleGrantCapability grants an island permission to invoke a named host
// capability target (a macOS Shortcut, or a ~/.dejima/capabilities/ script). The
// target's existence on the host is not required at grant time — a grant for a
// not-yet-present target is recorded and simply fails closed at execution until
// the target exists (the operator may grant ahead of authoring the Shortcut).
func (s *Server) handleGrantCapability(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req CapabilityGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := project.ValidateCapabilityTarget(req.Target); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	grant, err := p.AddCapabilityGrant(project.CapabilityGrant{Target: req.Target, GrantedAt: time.Now().UTC()})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "capability.grant", Island: name, Scope: grant.Target, Decision: "allowed",
	})
	writeJSON(w, http.StatusCreated, capabilityView(grant))
}

// handleRevokeCapability drops a capability grant by its target name.
func (s *Server) handleRevokeCapability(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	target := r.PathValue("target")
	removed, ok := p.RemoveCapabilityGrant(target)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no capability grant %q", name, target))
		return
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.ledgerAppend(ledger.Entry{
		Type: "capability.revoke", Island: name, Scope: removed.Target, Decision: "allowed",
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleCapabilityExecute runs a granted capability target on the island's
// behalf. Token callers (the in-island brain) are pinned to their own island by
// the bearer token; operators name the island in the body. Deny-all: the island
// must hold a grant for the target. Every outcome is ledgered.
func (s *Server) handleCapabilityExecute(w http.ResponseWriter, r *http.Request) {
	var req CapabilityExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// The token pins the island; an operator (unix socket / tailnet) names it.
	island := TokenIslandFromContext(r.Context())
	if island == "" {
		island = req.Island
	}
	if island == "" {
		writeError(w, http.StatusBadRequest, errors.New("island is required"))
		return
	}
	if err := project.ValidateCapabilityTarget(req.Target); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateCapArgs(req.Args); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	p, err := project.Load(island)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if _, ok := p.CapabilityGrantByTarget(req.Target); !ok {
		s.ledgerAppend(ledger.Entry{
			Type: "capability.deny", Island: island, Scope: req.Target,
			Decision: "denied", Detail: "not granted",
		})
		writeError(w, http.StatusForbidden, fmt.Errorf("capability %q is not granted to island %q", req.Target, island))
		return
	}

	adapter, err := s.capabilityAdapter()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("capability execution unavailable on this host: %w", err))
		return
	}
	res, execErr := adapter.Execute(r.Context(), capability.Request{
		Island: island, Target: req.Target, Args: req.Args,
	})
	if execErr != nil {
		status, reason := classifyCapError(execErr)
		s.ledgerAppend(ledger.Entry{
			Type: "capability.deny", Island: island, Scope: req.Target,
			Decision: "denied", Detail: reason,
		})
		writeError(w, status, execErr)
		return
	}

	var seq uint64
	if lg, lerr := ledger.Default(); lerr == nil {
		if e, aerr := lg.Append(ledger.Entry{
			Type: "capability.execute", Island: island, Scope: req.Target,
			SHA256:   argsHash(req.Args),
			Detail:   fmt.Sprintf("%s exit=%d", adapter.Name(), res.ExitCode),
			Decision: "allowed",
		}); aerr == nil {
			seq = e.Seq
		}
	}
	writeJSON(w, http.StatusOK, CapabilityExecuteResponse{
		OK: res.ExitCode == 0, Output: res.Output, ExitCode: res.ExitCode, LedgerSeq: seq,
	})
}

// classifyCapError maps an adapter error to an HTTP status + a short ledger
// reason. A target that ran and exited non-zero is NOT an error (it returns a
// Result), so it never reaches here.
func classifyCapError(err error) (int, string) {
	switch {
	case errors.Is(err, capability.ErrTargetNotFound):
		return http.StatusNotFound, "target not found"
	case errors.Is(err, capability.ErrTargetUntrusted):
		return http.StatusForbidden, "target untrusted"
	case errors.Is(err, capability.ErrTimeout):
		return http.StatusGatewayTimeout, "timeout"
	default:
		return http.StatusUnprocessableEntity, "exec failed"
	}
}

func validateCapArgs(args map[string]string) error {
	if len(args) > maxCapArgs {
		return fmt.Errorf("too many args (%d > %d)", len(args), maxCapArgs)
	}
	for k, v := range args {
		if k == "" || len(k) > maxCapKeyLen {
			return fmt.Errorf("arg key must be 1–%d chars", maxCapKeyLen)
		}
		if len(v) > maxCapValueLen {
			return fmt.Errorf("arg %q value exceeds %d bytes", k, maxCapValueLen)
		}
	}
	return nil
}

// argsHash is a canonical SHA-256 of the args map — what the ledger records
// instead of the raw args (tractable + PII-free, like a file trade's content
// hash). Empty for no args.
func argsHash(args map[string]string) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(args[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func capabilityView(c project.CapabilityGrant) CapabilityGrantView {
	return CapabilityGrantView{Target: c.Target, GrantedAt: c.GrantedAt}
}

func capabilityViews(cs []project.CapabilityGrant) []CapabilityGrantView {
	out := make([]CapabilityGrantView, 0, len(cs))
	for _, c := range cs {
		out = append(out, capabilityView(c))
	}
	return out
}
