package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// TestUsageSignals locks the resource-vs-cap + container-health contract the TUI
// consumes:
//   - cached container stats (mem/cpu) are surfaced on BOTH list and detail;
//   - configured resource caps are surfaced on BOTH list and detail (so the
//     client can compute "% of cap" from raw used + cap);
//   - container crash health (oom/restart/exit) is detail-only.
func TestUsageSignals(t *testing.T) {
	h, f := newTestServer(t) // HOME → temp dir
	p := &project.Project{
		Name:         "isl",
		DesiredState: project.StateRunning,
		Resources:    project.Resources{Memory: "2g", CPUs: "1.5", Disk: "20g"},
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	f.status = runtime.StatusRunning
	f.statsByName = map[string]runtime.Stats{
		p.ContainerName(): {MemoryUsageBytes: 512 << 20, MemoryLimitBytes: 2 << 30, CPUPercent: 12.5},
	}
	f.health = runtime.Health{OOMKilled: true, RestartCount: 3, ExitCode: 137}

	// --- LIST: stats + caps present, health absent ---
	rr := do(t, h, http.MethodGet, "/v1/islands", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	var list []IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	li := list[0]
	if li.Stats == nil {
		t.Fatal("list: Stats is nil, want cached mem/cpu")
	}
	if li.Stats.MemoryUsageBytes != 512<<20 || li.Stats.MemoryLimitBytes != 2<<30 || li.Stats.CPUPercent != 12.5 {
		t.Errorf("list stats = %+v, want used=512MiB limit=2GiB cpu=12.5", li.Stats)
	}
	if li.Resources == nil {
		t.Fatal("list: Resources caps nil, want configured caps for client-side %")
	}
	if li.Resources.Memory != "2g" || li.Resources.CPUs != "1.5" || li.Resources.Disk != "20g" {
		t.Errorf("list resources = %+v, want mem=2g cpus=1.5 disk=20g", li.Resources)
	}
	if li.Health != nil {
		t.Error("list: Health populated; container health is detail-only")
	}

	// --- DETAIL: stats + caps + health all present ---
	rr = do(t, h, http.MethodGet, "/v1/islands/isl", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rr.Code, rr.Body.String())
	}
	var info IslandInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Stats == nil || info.Stats.CPUPercent != 12.5 {
		t.Errorf("detail stats = %+v, want cpu=12.5", info.Stats)
	}
	if info.Resources == nil || info.Resources.Memory != "2g" {
		t.Errorf("detail resources = %+v, want mem=2g", info.Resources)
	}
	if info.Health == nil {
		t.Fatal("detail: Health nil, want container crash health")
	}
	if !info.Health.OOMKilled || info.Health.RestartCount != 3 || info.Health.ExitCode != 137 {
		t.Errorf("detail health = %+v, want oom=true restarts=3 exit=137", info.Health)
	}
}
