package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// handlePortIntake brokers a single host file (within a granted scope) into the
// island, read-only. The crossing is ledgered fail-closed: if the audit record
// cannot be written, the file does not cross.
func (s *Server) handlePortIntake(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req PortIntakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scope, ok := p.PortScopeByName(req.Scope)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no Port scope %q (access is deny-all; grant one with `dejima port grant`)", name, req.Scope))
		return
	}
	real, rel, err := resolveWithinScope(scope.HostPath, req.SrcRel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	info, err := os.Stat(real)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("source not reachable on the daemon host: %w", err))
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, fmt.Errorf("intake is single-file in V1; %q is a directory", rel))
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a regular file", rel))
		return
	}
	if status, _ := s.rt.Status(r.Context(), p.ContainerName()); status != runtime.StatusRunning {
		writeError(w, http.StatusConflict, errIslandNotRunning(name))
		return
	}
	size, sum, err := hashFile(real)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dest := req.Dest
	if dest == "" {
		dest = "/intake/" + scope.Name + "/" + filepath.ToSlash(rel)
	}

	// Fail closed: record the crossing before any byte enters the island.
	if err := s.ledgerAppend(ledger.Entry{
		Type: "trade.read", Island: name, Scope: scope.Name, Path: rel,
		Mode: scope.Mode, Bytes: size, SHA256: sum, Decision: "allowed",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("refusing to Trade: ledger write failed: %w", err))
		return
	}

	_, _, _, _ = s.rt.Exec(r.Context(), p.ContainerName(), []string{"mkdir", "-p", pathpkg.Dir(dest)})
	if err := s.rt.CopyToContainer(r.Context(), p.ContainerName(), real, dest); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, PortIntakeResponse{Scope: scope.Name, Src: real, Dest: dest, Bytes: size, SHA256: sum})
}

// handlePortExport copies a file out of the island into the host-owned export
// staging area. It never touches a user scope (writing into a scope is the
// read-write milestone), so it is always safe under read-only V1.
func (s *Server) handlePortExport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req PortExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	src := "/" + strings.TrimPrefix(req.Src, "/")
	base := pathpkg.Base(src)
	if base == "/" || base == "." || base == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid source path %q", req.Src))
		return
	}
	if status, _ := s.rt.Status(r.Context(), p.ContainerName()); status != runtime.StatusRunning {
		writeError(w, http.StatusConflict, errIslandNotRunning(name))
		return
	}
	if err := project.EnsureProjectSubdirs(name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dir, err := paths.ProjectDir(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dest := filepath.Join(dir, "exports", base)
	if err := s.rt.CopyFromContainer(r.Context(), p.ContainerName(), src, dest); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	size, sum, err := hashFile(dest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.ledgerAppend(ledger.Entry{
		Type: "trade.export", Island: name, Path: src, Bytes: size,
		SHA256: sum, Decision: "allowed", Detail: dest,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("exported to staging but ledger write failed: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, PortExportResponse{Src: src, Dest: dest, Bytes: size, SHA256: sum})
}

// resolveWithinScope joins rel onto the scope root and verifies, after symlink
// resolution, that the result stays inside the scope. Returns the real host
// path and the clean scope-relative path.
func resolveWithinScope(root, rel string) (realPath, relClean string, err error) {
	if filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("path must be relative to the scope, got absolute %q", rel)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("scope root unreadable: %w", err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(realRoot, rel))
	if err != nil {
		return "", "", fmt.Errorf("path %q not reachable within scope: %w", rel, err)
	}
	rc, err := filepath.Rel(realRoot, real)
	if err != nil || rc == ".." || strings.HasPrefix(rc, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes the scope", rel)
	}
	return real, rc, nil
}

// hashFile returns the size and hex SHA-256 of a host file.
func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
