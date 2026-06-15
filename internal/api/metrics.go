package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/version"
)

// islandMetric is the per-island snapshot the /metrics endpoint exposes.
type islandMetric struct {
	name, owner string
	up          bool
	mem         uint64
	memLimit    uint64
	cpu         float64
	wsBytes     int64
	homeBytes   int64
	restarts    int
	oom         bool
	attached    int
}

// handleMetrics serves a Prometheus text-exposition snapshot of fleet state:
// islands-by-state, per-island cpu/mem/disk, restart/OOM counts, attached
// clients, panic state, and daemon build info. Hand-rolled (no client_golang
// dependency) to match the project's dependency-light stance. Operator-level —
// the token-auth path default-denies it, so islands can't scrape the fleet.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	projects, err := project.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ctx := r.Context()
	stats := s.statsAll(ctx)
	sizes := s.volumeSizes(ctx)

	stateCount := map[string]int{"running": 0, "hibernated": 0, "errored": 0}
	ims := make([]islandMetric, 0, len(projects))
	for _, p := range projects {
		status, _ := s.rt.Status(ctx, p.ContainerName())
		im := islandMetric{name: p.Name, owner: p.Owner}
		switch status {
		case runtime.StatusRunning:
			stateCount["running"]++
			im.up = true
		case runtime.StatusErrored, runtime.StatusMissing:
			stateCount["errored"]++
		default:
			if p.DesiredState == project.StateHibernated {
				stateCount["hibernated"]++
			}
		}
		if st, ok := stats[p.ContainerName()]; ok {
			im.mem, im.memLimit, im.cpu = st.MemoryUsageBytes, st.MemoryLimitBytes, st.CPUPercent
		}
		if h, err := s.rt.Inspect(ctx, p.ContainerName()); err == nil {
			im.restarts, im.oom = h.RestartCount, h.OOMKilled
		}
		if sizes != nil {
			im.wsBytes, im.homeBytes = sizes[p.WorkspaceVolume()], sizes[p.HomeVolume()]
		}
		im.attached = len(s.islandPresence(p.Name))
		ims = append(ims, im)
	}
	// Stable output so diffs/scrapes are deterministic.
	sort.Slice(ims, func(i, j int) bool { return ims[i].name < ims[j].name })

	var b strings.Builder
	writeMetrics(&b, stateCount, ims, panicEngaged())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// writeMetrics renders the exposition text. Split out (pure) so it's testable
// without a runtime.
func writeMetrics(b *strings.Builder, stateCount map[string]int, ims []islandMetric, panicked bool) {
	fmt.Fprintf(b, "# HELP dejima_daemon_info Daemon build info (constant 1; version in labels).\n")
	fmt.Fprintf(b, "# TYPE dejima_daemon_info gauge\n")
	fmt.Fprintf(b, "dejima_daemon_info{version=%q,api_version=\"%d\"} 1\n", version.Version, version.APIVersion)

	fmt.Fprintf(b, "# HELP dejima_panicked 1 when the daemon is in panic mode (all islands stopped).\n")
	fmt.Fprintf(b, "# TYPE dejima_panicked gauge\n")
	fmt.Fprintf(b, "dejima_panicked %d\n", b2i(panicked))

	fmt.Fprintf(b, "# HELP dejima_islands Number of islands by observed state.\n")
	fmt.Fprintf(b, "# TYPE dejima_islands gauge\n")
	for _, st := range []string{"running", "hibernated", "errored"} {
		fmt.Fprintf(b, "dejima_islands{state=%q} %d\n", st, stateCount[st])
	}

	// Per-island families. Emit each family's header once, then all samples.
	family(b, "dejima_island_up", "1 when the island's container is running.", "gauge", ims,
		func(im islandMetric) string { return fmt.Sprintf("%d", b2i(im.up)) })
	family(b, "dejima_island_memory_usage_bytes", "Island container memory usage in bytes.", "gauge", ims,
		func(im islandMetric) string { return fmt.Sprintf("%d", im.mem) })
	family(b, "dejima_island_memory_limit_bytes", "Island container memory limit in bytes (0 = unlimited).", "gauge", ims,
		func(im islandMetric) string { return fmt.Sprintf("%d", im.memLimit) })
	family(b, "dejima_island_cpu_percent", "Island container CPU usage percent.", "gauge", ims,
		func(im islandMetric) string { return trimFloat(im.cpu) })
	family(b, "dejima_island_restart_count", "Island container restart count (flapping/OOM signal).", "gauge", ims,
		func(im islandMetric) string { return fmt.Sprintf("%d", im.restarts) })
	family(b, "dejima_island_oom_killed", "1 when the island's last run was OOM-killed.", "gauge", ims,
		func(im islandMetric) string { return fmt.Sprintf("%d", b2i(im.oom)) })
	family(b, "dejima_island_attached_clients", "Number of clients currently attached to the island.", "gauge", ims,
		func(im islandMetric) string { return fmt.Sprintf("%d", im.attached) })

	// Disk is a two-volume family (an extra `volume` label).
	fmt.Fprintf(b, "# HELP dejima_island_disk_bytes Island volume size in bytes (0 if the driver doesn't report it).\n")
	fmt.Fprintf(b, "# TYPE dejima_island_disk_bytes gauge\n")
	for _, im := range ims {
		fmt.Fprintf(b, "dejima_island_disk_bytes{island=%q,owner=%q,volume=\"workspace\"} %d\n", im.name, im.owner, im.wsBytes)
		fmt.Fprintf(b, "dejima_island_disk_bytes{island=%q,owner=%q,volume=\"home\"} %d\n", im.name, im.owner, im.homeBytes)
	}
}

// family writes one per-island metric family: a HELP/TYPE header followed by an
// {island,owner}-labeled sample per island.
func family(b *strings.Builder, name, help, typ string, ims []islandMetric, val func(islandMetric) string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
	for _, im := range ims {
		fmt.Fprintf(b, "%s{island=%q,owner=%q} %s\n", name, im.name, im.owner, val(im))
	}
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// trimFloat renders a float without a trailing ".0" noise, Prometheus-friendly.
func trimFloat(f float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.2f", f), ".00")
}
