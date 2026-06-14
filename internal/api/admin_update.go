package api

// admin_update.go serves POST /v1/admin/update — the daemon updating *itself*
// (the server-side half of `dejima update`, drivable from the TUI including a
// remote one). This is an operator route: it is NOT in tokenRouteAccess, so the
// in-island token path (tokenauth.go, default-deny) can never reach it — only
// operator callers (the local unix socket and tailnet-pinned TCP, same trust as
// delete-island / build-image). Provenance for the release path is the published
// SHA256SUMS (see selfupdate/release.go); the source path refuses a dirty tree.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aoos/dejima/internal/selfupdate"
)

func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	var req AdminUpdateRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body → dry run

	st, err := selfupdate.Check(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("check for updates: %w", err))
		return
	}
	resp := AdminUpdateResponse{
		Current:         st.Current,
		Latest:          st.Latest,
		Mode:            string(st.Mode),
		UpdateAvailable: st.UpdateAvailable,
	}
	if !req.Execute || !st.UpdateAvailable {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Validate we can actually apply *before* promising to, so the client gets a
	// real error instead of a silent background failure.
	meta, _ := selfupdate.LoadInstallMeta()
	if st.Mode == selfupdate.ModeSource && meta.SourceDir == "" {
		writeError(w, http.StatusPreconditionFailed, errors.New(
			"source install but no recorded checkout — re-run `dejima service install` to record it, or update manually"))
		return
	}
	if st.Mode == selfupdate.ModeSource {
		if err := selfupdate.SourcePreflight(r.Context(), meta.SourceDir); err != nil {
			writeError(w, http.StatusPreconditionFailed, err)
			return
		}
	}

	resp.Applying = true
	writeJSON(w, http.StatusOK, resp)
	if f, ok := w.(http.Flusher); ok {
		f.Flush() // get the response to the client before the restart drops the conn
	}

	// Apply off the request path: the restart terminates this process, so the
	// response above is the client's confirmation that it began.
	go s.applyDaemonUpdate(st, meta)
}

// applyDaemonUpdate performs the update and restarts the service. It runs in its
// own goroutine because the final restart kills this process; the new binary is
// brought up by the service manager (launchd/systemd).
func (s *Server) applyDaemonUpdate(st selfupdate.Status, meta selfupdate.InstallMeta) {
	ctx := context.Background()
	out := &slogWriter{log: s.log}
	run := selfupdate.ExecRunner(out)

	switch st.Mode {
	case selfupdate.ModeSource:
		s.log.Info("daemon self-update (source)", "dir", meta.SourceDir, "to", st.Latest)
		if err := run(ctx, meta.SourceDir, "git", "pull", "--ff-only"); err != nil {
			s.log.Error("daemon self-update: git pull failed", "err", err)
			return
		}
		if err := run(ctx, meta.SourceDir, "make", "install"); err != nil {
			s.log.Error("daemon self-update: make install failed", "err", err)
			return
		}
	case selfupdate.ModeRelease:
		s.log.Info("daemon self-update (release)", "to", st.Latest)
		if err := selfupdate.ApplyReleaseSelf(ctx, st.Latest, out); err != nil {
			s.log.Error("daemon self-update: binary replace failed", "err", err)
			return
		}
	default:
		s.log.Error("daemon self-update: unknown mode", "mode", st.Mode)
		return
	}

	// Restart in the right domain (system installs need --system). This kills us;
	// the service manager starts the freshly-installed binary.
	args := meta.RestartArgs()
	s.log.Info("daemon self-update: restarting", "cmd", "dejima "+strings.Join(args, " "))
	if err := run(ctx, "", "dejima", args...); err != nil {
		s.log.Error("daemon self-update: restart failed — restart manually", "err", err)
	}
}

// slogWriter adapts a slog.Logger to io.Writer so command output from the update
// steps lands in the daemon log.
type slogWriter struct{ log *slog.Logger }

func (w *slogWriter) Write(p []byte) (int, error) {
	if line := strings.TrimRight(string(p), "\n"); line != "" {
		w.log.Info("update", "out", line)
	}
	return len(p), nil
}
