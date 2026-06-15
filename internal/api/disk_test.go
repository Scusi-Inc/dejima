package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

// TestIslandDiskUsage: the detail endpoint reports per-island volume sizes from
// the runtime, keyed by the island's workspace and home volume names.
func TestIslandDiskUsage(t *testing.T) {
	h, f := newTestServer(t) // HOME → temp dir
	p := &project.Project{Name: "isl", DesiredState: project.StateRunning}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f.volumeSizes = map[string]int64{
		p.WorkspaceVolume(): 5 << 20, // 5 MiB
		p.HomeVolume():      3 << 20, // 3 MiB
		"some-other-vol":    9 << 30, // must be ignored
	}

	rr := do(t, h, http.MethodGet, "/v1/islands/isl", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rr.Code, rr.Body.String())
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Disk == nil {
		t.Fatal("Disk is nil, want sizes")
	}
	if info.Disk.WorkspaceBytes != 5<<20 || info.Disk.HomeBytes != 3<<20 {
		t.Errorf("disk = %+v, want ws=5MiB home=3MiB", info.Disk)
	}
	if info.Disk.TotalBytes != (5<<20)+(3<<20) {
		t.Errorf("total = %d, want %d", info.Disk.TotalBytes, (5<<20)+(3<<20))
	}

	// The list endpoint stays cheap — no disk field there.
	rr = do(t, h, http.MethodGet, "/v1/islands", "")
	var list []IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list) == 1 && list[0].Disk != nil {
		t.Error("list endpoint populated Disk; should be detail-only")
	}
}
