package api

import (
	"context"
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
		// Default into the agent's home — writable by the island's dejima user.
		// The container root is root-owned, so a top-level /intake isn't creatable
		// by the agent and docker cp would fail.
		dest = "/home/dejima/intake/" + scope.Name + "/" + filepath.ToSlash(rel)
	}

	// Fail closed: record the crossing before any byte enters the island.
	if err := s.ledgerAppend(ledger.Entry{
		Type: "trade.read", Island: name, Scope: scope.Name, Path: rel,
		Mode: scope.Mode, Bytes: size, SHA256: sum, Decision: "allowed",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("refusing to Trade: ledger write failed: %w", err))
		return
	}

	// Copy through a daemon-owned temp set 0644 so the in-island agent can read
	// the file regardless of the host file's owner/mode. docker cp preserves the
	// source uid+mode, and the agent isn't the file's owner (so it couldn't
	// chmod it itself) — a host 0600 file would otherwise land unreadable. The
	// file is already inside the containment boundary, so in-island read bits add
	// no exposure; the broker decided what crosses.
	staged, err := copyToTempReadable(real)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(staged)

	_, _, _, _ = s.rt.Exec(r.Context(), p.ContainerName(), []string{"mkdir", "-p", pathpkg.Dir(dest)})
	if err := s.rt.CopyToContainer(r.Context(), p.ContainerName(), staged, dest); err != nil {
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

// handlePortWrite brokers a file OUT of the island INTO a read-write scope on
// the host — the inverse of intake. Write-through-symlink and ../ escapes are
// refused; the crossing is ledgered fail-closed (no byte is written to the host
// if the audit record can't be written).
func (s *Server) handlePortWrite(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req PortWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	scope, ok := p.PortScopeByName(req.Scope)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no Port scope %q", name, req.Scope))
		return
	}
	if scope.Mode != project.PortModeRW {
		writeError(w, http.StatusForbidden, fmt.Errorf("scope %q is read-only; re-grant it :rw to write", scope.Name))
		return
	}
	target, rel, err := resolveWriteTarget(scope.HostPath, req.DestRel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	src := "/" + strings.TrimPrefix(req.Src, "/")
	if status, _ := s.rt.Status(r.Context(), p.ContainerName()); status != runtime.StatusRunning {
		writeError(w, http.StatusConflict, errIslandNotRunning(name))
		return
	}

	// Stage the island file on the host, then validate + hash before it touches
	// the user's scope.
	staged, cleanup, err := s.stageFromContainer(r.Context(), p.ContainerName(), src)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer cleanup()
	size, sum, err := hashFile(staged)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// Fail closed: record the write before any byte lands in the user's scope.
	if err := s.ledgerAppend(ledger.Entry{
		Type: "trade.write", Island: name, Scope: scope.Name, Path: rel,
		Mode: scope.Mode, Bytes: size, SHA256: sum, Decision: "allowed",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("refusing to write: ledger write failed: %w", err))
		return
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := copyFileTo(staged, target); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, PortWriteResponse{Scope: scope.Name, Src: src, Dest: target, Bytes: size, SHA256: sum})
}

// resolveWriteTarget validates a write destination within a scope. The target
// file need not exist; ../ escapes are refused lexically, the deepest existing
// ancestor is symlink-resolved and confirmed inside the scope, and an existing
// symlink at the target is refused (no writing *through* a symlink out of scope).
func resolveWriteTarget(root, rel string) (target, relClean string, err error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("destination must be a relative path within the scope, got %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("destination %q escapes the scope", rel)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("scope root unreadable: %w", err)
	}
	target = filepath.Join(realRoot, clean)
	// Walk up to the deepest ancestor that exists, resolve its symlinks, and
	// confirm it is still inside the scope (catches a symlinked subdir).
	for anc := filepath.Dir(target); ; anc = filepath.Dir(anc) {
		real, err := filepath.EvalSymlinks(anc)
		if err == nil {
			rc, rerr := filepath.Rel(realRoot, real)
			if rerr != nil || rc == ".." || strings.HasPrefix(rc, ".."+string(filepath.Separator)) {
				return "", "", fmt.Errorf("destination %q escapes the scope", rel)
			}
			break
		}
		if anc == realRoot || anc == "/" || anc == "." || filepath.Dir(anc) == anc {
			break
		}
	}
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("destination %q is a symlink; refusing to write through it", rel)
	}
	return target, clean, nil
}

// stageFromContainer copies a container file to a fresh host temp dir, returning
// the staged path and a cleanup func.
func (s *Server) stageFromContainer(ctx context.Context, container, src string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "dejima-write-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	staged := filepath.Join(dir, "staged")
	if err := s.rt.CopyFromContainer(ctx, container, src, staged); err != nil {
		cleanup()
		return "", nil, err
	}
	return staged, cleanup, nil
}

// copyFileTo writes src's contents to dst (truncating), mode 0644.
func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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

// copyToTempReadable copies a host file to a fresh daemon-owned temp set 0644,
// returning its path (caller removes it). Used so a brokered file lands readable
// by the in-island agent even when the host file is 0600 — docker cp preserves
// the source uid+mode and the agent isn't the owner, so it couldn't relax the
// mode itself.
func copyToTempReadable(real string) (string, error) {
	src, err := os.Open(real)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := os.CreateTemp("", "dejima-intake-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
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
