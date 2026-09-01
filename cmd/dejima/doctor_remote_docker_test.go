package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

func fakeDaemon(t *testing.T, dockerUp, imageUp bool) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "overview") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.OverviewResponse{
				DockerReachable:    dockerUp,
				IslandImagePresent: imageUp,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("DEJIMA_HOST", ts.URL)
}

func findCheck(r *doctorReport, name string) (string, string, bool) {
	for _, row := range r.rows {
		if row.check == name {
			return row.status, row.detail + " " + row.fix, true
		}
	}
	return "", "", false
}

// A client driving a remote daemon whose Docker is dead must be TOLD so.
// Reporting "docker runs on the daemon host, not here" is true, useless, and
// was the actual behaviour while a host sat broken for two weeks.
func TestDoctorReportsRemoteDockerDown(t *testing.T) {
	fakeDaemon(t, false, true)
	r := &doctorReport{}
	checkDocker(context.Background(), r)

	status, text, found := findCheck(r, "docker")
	if !found {
		t.Fatal("doctor reported nothing about docker at all")
	}
	if status != "FAIL" {
		t.Errorf("docker status = %q, want FAIL — the daemon said Docker was unreachable", status)
	}
	if !strings.Contains(text, "docker desktop start") {
		t.Errorf("no actionable remedy in:\n%s", text)
	}
	// The remedy must survive the "it says it's already running" trap, which is
	// what actually happened and what sent the operator toward a reinstall.
	if !strings.Contains(text, "hung") {
		t.Errorf("remedy doesn't cover a hung Docker claiming to be running:\n%s", text)
	}
}

func TestDoctorReportsRemoteDockerUp(t *testing.T) {
	fakeDaemon(t, true, true)
	r := &doctorReport{}
	checkDocker(context.Background(), r)

	status, _, found := findCheck(r, "docker")
	if !found {
		t.Fatal("doctor reported nothing about docker")
	}
	if status != "OK" {
		t.Errorf("docker status = %q, want OK — the daemon said Docker was reachable", status)
	}
}
