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
	"os"
	"runtime"
	"strings"
	"time"

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
	// A system install reinstalls + restarts via sudo with no TTY, which only
	// works with the scoped /etc/sudoers.d/dejima drop-in. If it's missing (an
	// install predating that feature — the exact trap hit in dogfooding), every
	// step would hit a password prompt and fail. Catch it now with an actionable
	// message instead of a cryptic mid-build error.
	if err := preflightPrivileged(meta); err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}

	// Build/install the new binary SYNCHRONOUSLY, on a context detached from the
	// request (a flaky client connection mustn't abort a half-done install).
	// Doing the work here — rather than in a detached goroutine that returns
	// Applying=true unconditionally — means git pull / make install / download
	// failures come back to the caller as real errors, instead of vanishing into
	// the daemon log while the TUI cheerfully reports "updating…".
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	out := &slogWriter{log: s.log}
	if err := s.prepareDaemonUpdate(ctx, st, meta, out); err != nil {
		s.log.Error("daemon self-update: prepare failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// New binary is in place. Acknowledge, then restart asynchronously — the
	// restart kills this process, so it can't be part of the response.
	resp.Applying = true
	writeJSON(w, http.StatusOK, resp)
	if f, ok := w.(http.Flusher); ok {
		f.Flush() // get the ack out before the restart drops the conn
	}
	go s.restartDaemon(meta)
}

// prepareDaemonUpdate brings the new binary into place WITHOUT restarting — the
// part that can fail and must be reported. Source installs git-pull + make
// install; release installs download + verify + replace.
func (s *Server) prepareDaemonUpdate(ctx context.Context, st selfupdate.Status, meta selfupdate.InstallMeta, out *slogWriter) error {
	switch st.Mode {
	case selfupdate.ModeSource:
		s.log.Info("daemon self-update (source): building", "dir", meta.SourceDir, "to", st.Latest)
		return selfupdate.PrepareSource(ctx, meta.SourceDir, out, selfupdate.ExecRunner(out))
	case selfupdate.ModeRelease:
		s.log.Info("daemon self-update (release): downloading", "to", st.Latest)
		return selfupdate.ApplyReleaseSelf(ctx, st.Latest, out)
	default:
		return fmt.Errorf("unknown update mode %q", st.Mode)
	}
}

// restartDaemon restarts the service in the right domain (system installs need
// --system). It runs in its own goroutine because the restart kills this
// process; the service manager brings up the freshly-installed binary.
func (s *Server) restartDaemon(meta selfupdate.InstallMeta) {
	out := &slogWriter{log: s.log}
	args := meta.RestartArgs()
	s.log.Info("daemon self-update: restarting", "cmd", "dejima "+strings.Join(args, " "))
	if err := selfupdate.ExecRunner(out)(context.Background(), "", "dejima", args...); err != nil {
		// The new binary is already installed; a failed restart just means the old
		// process keeps running until the next restart/boot picks it up.
		s.log.Error("daemon self-update: restart failed — new binary installed, restart manually (e.g. sudo launchctl kickstart -k system/<label>)", "err", err)
	}
}

// preflightPrivileged fails fast when a privileged (system) self-update can't
// possibly succeed non-interactively. Today that's the macOS sudoers drop-in
// that `dejima service install --system` writes; without it, sudo prompts for a
// password the headless daemon can't supply.
func preflightPrivileged(meta selfupdate.InstallMeta) error {
	if !meta.System || runtime.GOOS != "darwin" {
		return nil
	}
	const sudoers = "/etc/sudoers.d/dejima" // mirrors internal/service.launchdSystemSudoers
	if _, err := os.Stat(sudoers); err != nil {
		return fmt.Errorf(
			"self-update needs passwordless sudo (%s is missing) — re-run `sudo dejima service install --system` to add it, then retry",
			sudoers)
	}
	return nil
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
