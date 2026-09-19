package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// Snapshotting the home volume before anything destroys it.
//
// WHY THIS EXISTS. An operator set a GH_TOKEN, was told to restart the agent,
// went looking for how, and ran `dejima reset`. Reset destroys the home volume —
// /home/dejima, which holds .codex and .claude — so every conversation in the
// island went with it. Irreversibly.
//
// Nothing was broken. reset warned, listed "all conversation history", and made
// them type the island name. `dejima eject --include-home` would have saved it.
// All of that requires the operator to already know what they are about to lose
// and to act BEFORE they lose it, which is precisely the knowledge they do not
// have at that moment. A warning transfers the risk to the person least equipped
// to price it.
//
// So the machine takes the copy. reset knows exactly which volume it is about to
// remove; taking a copy first costs one volume copy and turns "irreversible"
// into "restore it".
//
// FAILS CLOSED. If the snapshot cannot be taken, the destructive operation does
// not happen. A snapshot that is skipped on error is worse than none: it reads
// as protection right up until the one time it mattered.
const (
	// homeSnapshotKeep is how many snapshots survive per island. Enough to cover
	// "I reset again before noticing", bounded because these are full copies of a
	// home volume and an unbounded pile of them is its own outage.
	homeSnapshotKeep = 3
	homeSnapshotPfx  = "-home-snap-"
)

// homeSnapshotVolume names a snapshot volume for an island at a moment.
// Unix seconds rather than a formatted stamp: it sorts lexically in the same
// order it sorts numerically for the next ~250 years, which keeps pruning
// honest without parsing anything back.
func homeSnapshotVolume(island string, at time.Time) string {
	return "dejima-" + island + homeSnapshotPfx + strconv.FormatInt(at.Unix(), 10)
}

// snapshotHome copies the island's home volume aside and records it on the
// project. Returns the snapshot volume name.
//
// The caller must NOT proceed with a destructive operation if this errors.
func (s *Server) snapshotHome(ctx context.Context, p *project.Project, reason string) (string, error) {
	vol := homeSnapshotVolume(p.Name, time.Now())
	if err := s.rt.EnsureVolume(ctx, vol); err != nil {
		return "", fmt.Errorf("create snapshot volume: %w", err)
	}
	if err := s.rt.CopyVolumeData(ctx, p.HomeVolume(), vol, p.Image); err != nil {
		// Remove the empty volume we just made, so a failed snapshot does not
		// leave a convincing-looking artifact behind. An empty snapshot is worse
		// than an absent one — it restores cleanly, to nothing.
		_ = s.rt.RemoveVolume(ctx, vol, true)
		return "", fmt.Errorf("copy home volume: %w", err)
	}
	p.HomeSnapshots = append(p.HomeSnapshots, project.HomeSnapshot{
		Volume: vol, TakenAt: time.Now().UTC(), Reason: reason,
	})
	s.pruneHomeSnapshots(ctx, p)
	return vol, nil
}

// pruneHomeSnapshots drops all but the newest homeSnapshotKeep, removing the
// volumes it forgets. Best-effort on the removal: a volume that will not delete
// is a leak, and dropping the record anyway would make it an INVISIBLE leak.
func (s *Server) pruneHomeSnapshots(ctx context.Context, p *project.Project) {
	if len(p.HomeSnapshots) <= homeSnapshotKeep {
		return
	}
	sort.Slice(p.HomeSnapshots, func(i, j int) bool {
		return p.HomeSnapshots[i].TakenAt.After(p.HomeSnapshots[j].TakenAt)
	})
	var kept []project.HomeSnapshot
	for i, snap := range p.HomeSnapshots {
		if i < homeSnapshotKeep {
			kept = append(kept, snap)
			continue
		}
		if err := s.rt.RemoveVolume(ctx, snap.Volume, true); err != nil {
			s.log.Warn("snapshot prune: volume not removed; keeping the record so it stays visible",
				"island", p.Name, "volume", snap.Volume, "err", err)
			kept = append(kept, snap)
		}
	}
	p.HomeSnapshots = kept
}

// --- restore ---------------------------------------------------------------

// RestoreHomeRequest names the snapshot to put back. Empty Volume means the
// newest, which is what someone who has just realised what they did wants.
type RestoreHomeRequest struct {
	Volume string `json:"volume,omitempty"`
}

// RestoreHomeResponse reports what was restored.
type RestoreHomeResponse struct {
	Volume  string    `json:"volume"`
	TakenAt time.Time `json:"taken_at"`
}

// restoreHome puts a snapshot back over the island's home volume.
//
// The inverse of what reset does, and deliberately built from the same steps:
// stop, replace the volume's contents, recreate, reconcile. A restore that took
// a different route would be a second implementation of the thing that has to
// agree with the first.
//
// IT SNAPSHOTS BEFORE IT RESTORES. A restore destroys whatever is in the home
// volume now, which may be work done since the reset — a fresh login, a
// conversation started in the new empty island. Overwriting that silently would
// make this command the same bug it exists to undo.
func (s *Server) restoreHome(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req RestoreHomeRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	snap, ok := pickSnapshot(p.HomeSnapshots, req.Volume)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf(
			"no snapshot %q for island %q (it keeps the newest %d)", req.Volume, name, homeSnapshotKeep))
		return
	}

	if _, err := s.snapshotHome(r.Context(), p, "restore"); err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Errorf("refusing to restore: could not snapshot the CURRENT home volume first (%w). "+
				"Nothing was changed", err))
		return
	}

	wasRunning := false
	if st, err := s.rt.Status(r.Context(), p.ContainerName()); err == nil {
		wasRunning = st == runtime.StatusRunning
	}
	_ = s.rt.RemoveContainer(r.Context(), p.ContainerName(), true)
	if err := s.rt.RemoveVolume(r.Context(), p.HomeVolume(), true); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("clear home volume: %w", err))
		return
	}
	if err := s.rt.EnsureVolume(r.Context(), p.HomeVolume()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.rt.CopyVolumeData(r.Context(), snap.Volume, p.HomeVolume(), p.Image); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("copy snapshot back: %w", err))
		return
	}
	if err := s.createContainerForProject(r.Context(), p, "", false); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	leftStopped := false
	if !wasRunning && p.DesiredState == project.StateHibernated {
		_ = s.rt.StopContainer(r.Context(), p.ContainerName())
		leftStopped = true
	}
	p.LastUsedAt = time.Now().UTC()
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// resume=false for the same reason reset uses it: this handler wrote the
	// container's launch value one call above, so the literal is what it just
	// baked rather than an assumption about what was there.
	if !leftStopped {
		s.reconcileAgentsAsync(p, false)
	}
	writeJSON(w, http.StatusOK, RestoreHomeResponse{Volume: snap.Volume, TakenAt: snap.TakenAt})
}

// pickSnapshot resolves a requested volume, or the newest when none is named.
func pickSnapshot(snaps []project.HomeSnapshot, want string) (project.HomeSnapshot, bool) {
	if len(snaps) == 0 {
		return project.HomeSnapshot{}, false
	}
	if want == "" {
		newest := snaps[0]
		for _, s := range snaps[1:] {
			if s.TakenAt.After(newest.TakenAt) {
				newest = s
			}
		}
		return newest, true
	}
	for _, s := range snaps {
		if s.Volume == want {
			return s, true
		}
	}
	return project.HomeSnapshot{}, false
}
