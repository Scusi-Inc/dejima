package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestWriteMetrics(t *testing.T) {
	var b strings.Builder
	ims := []islandMetric{
		{name: "web", owner: "alice@laptop", up: true, mem: 1 << 20, memLimit: 4 << 20, cpu: 12.5,
			wsBytes: 5 << 20, homeBytes: 3 << 20, restarts: 2, oom: true, attached: 1},
		{name: `quo"te`, up: false},
	}
	writeMetrics(&b, map[string]int{"running": 1, "hibernated": 0, "errored": 1}, ims, true)
	out := b.String()

	must := []string{
		"# TYPE dejima_daemon_info gauge",
		"dejima_panicked 1",
		`dejima_islands{state="running"} 1`,
		`dejima_island_up{island="web",owner="alice@laptop"} 1`,
		`dejima_island_memory_usage_bytes{island="web",owner="alice@laptop"} 1048576`,
		`dejima_island_cpu_percent{island="web",owner="alice@laptop"} 12.50`,
		`dejima_island_restart_count{island="web",owner="alice@laptop"} 2`,
		`dejima_island_oom_killed{island="web",owner="alice@laptop"} 1`,
		`dejima_island_disk_bytes{island="web",owner="alice@laptop",volume="workspace"} 5242880`,
		`dejima_island_disk_bytes{island="web",owner="alice@laptop",volume="home"} 3145728`,
		`dejima_island_up{island="quo\"te",owner=""} 0`, // label-value escaping (raw string: \" is a literal backslash-quote)
	}
	for _, m := range must {
		if !strings.Contains(out, m) {
			t.Errorf("metrics output missing line:\n  %s\n--- full output ---\n%s", m, out)
		}
	}

	// HELP/TYPE must precede the first sample of each family (Prometheus parsers
	// require the header before samples). Spot-check one family's ordering.
	help := strings.Index(out, "# TYPE dejima_island_up")
	sample := strings.Index(out, "dejima_island_up{")
	if help < 0 || sample < 0 || help > sample {
		t.Errorf("dejima_island_up: TYPE header (%d) must precede first sample (%d)", help, sample)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	h, _ := newTestServer(t)
	rr := do(t, h, http.MethodGet, "/metrics", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain…", ct)
	}
	if !strings.Contains(rr.Body.String(), "dejima_daemon_info{") {
		t.Errorf("body missing dejima_daemon_info:\n%s", rr.Body.String())
	}
}
